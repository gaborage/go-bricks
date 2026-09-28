package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// authServer answers statuses in order, then 200, and records each request's
// Authorization header.
func authServer(t *testing.T, statuses ...int) (srv *httptest.Server, seen func() []string) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []string
	)
	srv = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mu.Lock()
		got = append(got, r.Header.Get(headerAuthorization))
		n := len(got)
		mu.Unlock()
		if n <= len(statuses) {
			w.WriteHeader(statuses[n-1])
			return
		}
		w.WriteHeader(nethttp.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func quietLogger() logger.Logger { return logger.New("error", false) }

func TestBuildBearerTokenFileRejectsUnusableInput(t *testing.T) {
	dir := t.TempDir()
	blank := writeTestFile(t, dir, "blank", []byte(" \t\v\f\u00a0\r\n"))
	multiline := writeTestFile(t, dir, "multiline", []byte("tok-a\ntok-b\n"))
	schemed := writeTestFile(t, dir, "schemed", []byte("Bearer tok-scheme\n"))
	bom := writeTestFile(t, dir, "bom", []byte("\ufefftok-bom"))
	oversized := writeTestFile(t, dir, "oversized", bytes.Repeat([]byte("a"), maxBearerTokenFileBytes+1))
	subdir := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(subdir, 0o700))
	missing := filepath.Join(dir, "absent")

	tests := []struct {
		name       string
		path       string
		wantMsg    string
		wantPath   bool
		pathSecret bool
		isErr      error
	}{
		{name: "missing_file", path: missing, wantMsg: "read file", wantPath: true, isErr: fs.ErrNotExist},
		{name: "whitespace_only_file", path: blank, wantMsg: "is empty", wantPath: true},
		{name: "interior_newline", path: multiline, wantMsg: "holds a byte other than visible ASCII", wantPath: true},
		{name: "scheme_copied_into_file", path: schemed, wantMsg: "holds a byte other than visible ASCII", wantPath: true},
		{name: "utf8_byte_order_mark", path: bom, wantMsg: "holds a byte other than visible ASCII", wantPath: true},
		{name: "directory", path: subdir, wantMsg: "not a regular file", wantPath: true, isErr: errNotRegularFile},
		{name: "over_size_cap", path: oversized, wantMsg: "larger than 65536 bytes", wantPath: true, isErr: errTokenFileTooLarge},
		{name: "empty_path", path: "", wantMsg: "requires a file path"},
		{name: "whitespace_only_path", path: " \n", wantMsg: "requires a file path"},
		{name: "swapped_jwt", path: "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJwcm9iZSJ9.c2lnbmF0dXJl", wantMsg: "looks like a token", pathSecret: true},
		{name: "swapped_opaque_token", path: "opaque-probe-4d2f9a7c1e", wantMsg: "looks like a token", pathSecret: true},
		{name: "swapped_jwt_with_surrounding_whitespace", path: " eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJwcm9iZSJ9.c2lnbmF0dXJl\n", wantMsg: "looks like a token", pathSecret: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewBuilder(quietLogger()).WithBearerTokenFile(tt.path).Build()
			require.Error(t, err)
			assert.Nil(t, c)
			assert.Contains(t, err.Error(), tt.wantMsg)
			if tt.wantPath {
				assert.Contains(t, err.Error(), strconv.Quote(tt.path))
			}
			if tt.pathSecret {
				assert.NotContains(t, err.Error(), strings.TrimSpace(tt.path), "a token passed as the path must not be echoed")
				assert.Contains(t, err.Error(), "write ./<name>", "the refusal must name the workaround for a bare file name")
			}
			if tt.isErr != nil {
				require.ErrorIs(t, err, tt.isErr)
			}
			for _, content := range []string{"\v", `\v`, "\u00a0", "tok-a", "tok-b", "tok-scheme", "tok-bom"} {
				assert.NotContains(t, err.Error(), content, "the error must never carry file contents")
			}
		})
	}
}

func TestBuildBearerTokenFileAcceptsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "token", []byte("tok-relative"))
	writeTestFile(t, dir, "partner.jwt", []byte("tok-relative"))
	t.Chdir(dir)

	for _, path := range []string{"./token", "partner.jwt", " ./token\n"} {
		_, err := NewBuilder(quietLogger()).WithBearerTokenFile(path).Build()
		require.NoError(t, err, path)
	}
}

func TestBuildBearerTokenFileAcceptsTokenAtSizeCap(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", bytes.Repeat([]byte("a"), maxBearerTokenFileBytes))
	_, err := NewBuilder(quietLogger()).WithBearerTokenFile(path).Build()
	require.NoError(t, err)
}

func TestLooksLikeTokenSeparatesTokensFromPaths(t *testing.T) {
	const jwt = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJwcm9iZSJ9.c2lnbmF0dXJl"
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "jwt", path: jwt, want: true},
		{name: "bare_word", path: "opaque-probe-4d2f9a7c1e", want: true},
		{name: "file_name_with_extension", path: "partner.jwt", want: false},
		{name: "relative_path", path: "./token", want: false},
		{name: "absolute_path", path: "/var/run/secrets/token", want: false},
		{name: "windows_path_without_extension", path: `C:\tokens\partner`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikeToken(tt.path))
		})
	}
}

func TestIsVisibleASCIIAcceptsOnlyVCHAR(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "vchar_bounds", value: "!~", want: true},
		{name: "vendor_token_shapes", value: "123|abc%2F:x=", want: true},
		{name: "space", value: "a b", want: false},
		{name: "tab", value: "a\tb", want: false},
		{name: "del", value: "a\x7fb", want: false},
		{name: "obs_text", value: "a\x80b", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isVisibleASCII(tt.value))
		})
	}
}

func TestBuildBearerTokenFileRejectsConflictingAuthorization(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-conflict"))

	tests := []struct {
		name  string
		setup func(*Builder) *Builder
	}{
		{name: "with_basic_auth", setup: func(b *Builder) *Builder { return b.WithBasicAuth("user", "pass") }},
		{name: "default_header_canonical", setup: func(b *Builder) *Builder { return b.WithDefaultHeader("Authorization", "Bearer x") }},
		{name: "default_header_lowercase", setup: func(b *Builder) *Builder { return b.WithDefaultHeader("authorization", "Bearer x") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.setup(NewBuilder(quietLogger())).WithBearerTokenFile(path)
			c, err := b.Build()
			require.Error(t, err)
			assert.Nil(t, c)
			assert.Contains(t, err.Error(), "WithBearerTokenFile cannot be combined")
			assert.NotContains(t, err.Error(), "tok-conflict")
		})
	}

	_, err := NewBuilder(quietLogger()).
		WithDefaultHeader("X-Authorization-Hint", "x").
		WithBearerTokenFile(path).
		Build()
	require.NoError(t, err, "only the Authorization header itself conflicts")
}

func TestBearerTokenFileEveryAttemptCarriesTrimmedToken(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("  tok-trimmed \n"))
	srv, seen := authServer(t, nethttp.StatusServiceUnavailable, nethttp.StatusBadGateway)

	c, err := NewBuilder(quietLogger()).
		WithRetries(2, time.Millisecond).
		WithBearerTokenFile(path).
		Build()
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), &Request{URL: srv.URL})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"Bearer tok-trimmed", "Bearer tok-trimmed", "Bearer tok-trimmed"}, seen())
}

func TestBearerTokenFilePerRequestAuthorizationWins(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-file"))

	tests := []struct {
		name string
		req  Request
		want string
	}{
		{name: "request_headers_lowercase_key", req: Request{Headers: map[string]string{"authorization": "Bearer per-request"}}, want: "Bearer per-request"},
		{name: "request_basic_auth", req: Request{Auth: &BasicAuth{Username: "u", Password: "p"}}, want: "Basic dTpw"},
		{name: "request_headers_empty_value", req: Request{Headers: map[string]string{"authorization": ""}}, want: ""},
		{name: "no_per_request_authorization", req: Request{}, want: "Bearer tok-file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, seen := authServer(t)
			c, err := NewBuilder(quietLogger()).WithBearerTokenFile(path).Build()
			require.NoError(t, err)

			req := tt.req
			req.URL = srv.URL
			_, err = c.Get(context.Background(), &req)
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, seen())
		})
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestBearerTokenFileNeverLeaksToken drives the real logger — its default
// SensitiveDataFilter, with a buffer as the sink — through payload logging, a
// retry and an error response.
func TestBearerTokenFileNeverLeaksToken(t *testing.T) {
	const token = "leakprobe-7f3c9e1d2b"
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	sink := &syncBuffer{}
	sinkCtx := zerolog.New(sink).WithContext(context.Background())
	log := logger.New("debug", false).WithContext(sinkCtx)

	path := writeTestFile(t, t.TempDir(), "token", []byte(token+"\n"))
	srv, seen := authServer(t, nethttp.StatusInternalServerError, nethttp.StatusOK, nethttp.StatusUnauthorized)
	c, err := NewBuilder(log).
		WithBearerTokenFile(path).
		WithLogPayloads(true).
		WithRetries(1, time.Millisecond).
		Build()
	require.NoError(t, err)

	req := &Request{URL: srv.URL, Headers: map[string]string{"X-Probe": "visible-control"}, Body: []byte(`{"a":1}`)}
	_, err = c.Post(context.Background(), req)
	require.NoError(t, err)
	_, callErr := c.Post(context.Background(), req)
	require.Error(t, callErr)

	require.Len(t, seen(), 3)
	for _, h := range seen() {
		require.Equal(t, "Bearer "+token, h, "the token must actually have been sent for this test to mean anything")
	}

	out := sink.String()
	assert.NotContains(t, out, token)
	assert.NotContains(t, callErr.Error(), token)

	var maskedHeaderLines int
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &line), "log line is not JSON: %s", raw)
		headers, ok := line["headers"].(map[string]any)
		if !ok || line["direction"] != "outbound" {
			continue
		}
		assert.Equal(t, []any{"visible-control"}, headers["X-Probe"], "payload logging must have reached the sink")
		assert.Equal(t, logger.DefaultMaskValue, headers[headerAuthorization])
		maskedHeaderLines++
	}
	assert.Equal(t, 3, maskedHeaderLines, "one debug header line per attempt")

	spans := tp.Exporter.GetSpans()
	require.NotEmpty(t, spans)
	for i := range spans {
		for _, kv := range spans[i].Attributes {
			assert.NotContains(t, kv.Value.String(), token, "span attribute %s", kv.Key)
		}
		for _, ev := range spans[i].Events {
			for _, kv := range ev.Attributes {
				assert.NotContains(t, kv.Value.String(), token, "span event %s attribute %s", ev.Name, kv.Key)
			}
		}
		assert.NotContains(t, spans[i].Status.Description, token)
	}
}
