package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
)

const bearerTestInterval = 30 * time.Second

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

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

// bearerBuilder wires the fake clock into WithBearerTokenFile's clock seam.
func bearerBuilder(log logger.Logger, path string, opts BearerTokenFileOptions, clk *fakeClock) *Builder {
	b := NewBuilder(log).WithBearerTokenFile(path, opts)
	b.bearer.now = clk.now
	return b
}

func quietLogger() logger.Logger { return logger.New("error", false) }

func mustGet(t *testing.T, c Client, url string) {
	t.Helper()
	_, err := c.Get(context.Background(), &Request{URL: url})
	require.NoError(t, err)
}

func TestBuildBearerTokenFileRejectsUnusableInput(t *testing.T) {
	dir := t.TempDir()
	blank := writeTestFile(t, dir, "blank", []byte(" \t\v\f\u00a0\r\n"))
	good := writeTestFile(t, dir, "good", []byte("tok-good"))
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
		opts       BearerTokenFileOptions
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
		{name: "negative_refresh_interval", path: good, opts: BearerTokenFileOptions{RefreshInterval: -time.Second}, wantMsg: "is negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewBuilder(quietLogger()).WithBearerTokenFile(tt.path, tt.opts).Build()
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
			for _, content := range []string{"\v", `\v`, "\u00a0", "tok-good", "tok-a", "tok-b", "tok-scheme", "tok-bom"} {
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
		_, err := NewBuilder(quietLogger()).WithBearerTokenFile(path, BearerTokenFileOptions{}).Build()
		require.NoError(t, err, path)
	}
}

func TestBuildBearerTokenFileAcceptsTokenAtSizeCap(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", bytes.Repeat([]byte("a"), maxBearerTokenFileBytes))
	_, err := NewBuilder(quietLogger()).WithBearerTokenFile(path, BearerTokenFileOptions{}).Build()
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
			b := tt.setup(NewBuilder(quietLogger())).WithBearerTokenFile(path, BearerTokenFileOptions{})
			c, err := b.Build()
			require.Error(t, err)
			assert.Nil(t, c)
			assert.Contains(t, err.Error(), "WithBearerTokenFile cannot be combined")
			assert.NotContains(t, err.Error(), "tok-conflict")
		})
	}

	_, err := NewBuilder(quietLogger()).
		WithDefaultHeader("X-Authorization-Hint", "x").
		WithBearerTokenFile(path, BearerTokenFileOptions{}).
		Build()
	require.NoError(t, err, "only the Authorization header itself conflicts")
}

func TestBearerTokenFileEveryAttemptCarriesTrimmedToken(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("  tok-trimmed \n"))
	srv, seen := authServer(t, nethttp.StatusServiceUnavailable, nethttp.StatusBadGateway)

	c, err := NewBuilder(quietLogger()).
		WithRetries(2, time.Millisecond).
		WithBearerTokenFile(path, BearerTokenFileOptions{}).
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
			c, err := NewBuilder(quietLogger()).WithBearerTokenFile(path, BearerTokenFileOptions{}).Build()
			require.NoError(t, err)

			req := tt.req
			req.URL = srv.URL
			_, err = c.Get(context.Background(), &req)
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, seen())
		})
	}
}

// kubeletTokenDir lays a token out the way the kubelet's atomic writer does:
// token -> ..data/token, ..data -> <versioned dir>; publish swaps ..data.
func kubeletTokenDir(t *testing.T) (dir string, publish func(string)) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the projected-volume symlink layout is a Linux kubelet pattern")
	}
	dir = t.TempDir()
	version := 0
	publish = func(tok string) {
		version++
		ts := fmt.Sprintf("..v%d", version)
		require.NoError(t, os.Mkdir(filepath.Join(dir, ts), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ts, "token"), []byte(tok+"\n"), 0o600))
		require.NoError(t, os.Symlink(ts, filepath.Join(dir, "..data_tmp")))
		require.NoError(t, os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")))
	}
	require.NoError(t, os.Symlink(filepath.Join("..data", "token"), filepath.Join(dir, "token")))
	return dir, publish
}

func TestBearerTokenFilePicksUpRotationAfterInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		wantWait time.Duration
		setup    func(t *testing.T) (path string, rotate func(string))
	}{
		{
			name:     "symlink_swap",
			interval: bearerTestInterval,
			wantWait: bearerTestInterval,
			setup: func(t *testing.T) (string, func(string)) {
				dir, publish := kubeletTokenDir(t)
				publish("tok-v1")
				return filepath.Join(dir, "token"), publish
			},
		},
		{
			name:     "in_place_write_default_interval",
			wantWait: DefaultBearerTokenRefreshInterval,
			setup: func(t *testing.T) (string, func(string)) {
				path := writeTestFile(t, t.TempDir(), "token", []byte("tok-v1"))
				return path, func(tok string) { require.NoError(t, os.WriteFile(path, []byte(tok), 0o600)) }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, rotate := tt.setup(t)
			srv, seen := authServer(t)
			clk := newFakeClock()
			c, err := bearerBuilder(quietLogger(), path, BearerTokenFileOptions{RefreshInterval: tt.interval}, clk).Build()
			require.NoError(t, err)

			// Rotated before the first request: Build's deadline, not the request, decides.
			rotate("tok-v2")
			mustGet(t, c, srv.URL)
			clk.advance(tt.wantWait - time.Nanosecond)
			mustGet(t, c, srv.URL)
			clk.advance(time.Nanosecond)
			mustGet(t, c, srv.URL)

			assert.Equal(t, []string{"Bearer tok-v1", "Bearer tok-v1", "Bearer tok-v2"}, seen())
		})
	}
}

func TestBearerTokenFileRetryCarriesTokenRotatedBetweenAttempts(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-v1"))
	clk := newFakeClock()
	var (
		mu  sync.Mutex
		got []string
	)
	// The first attempt rotates the file and lets the interval lapse before it
	// answers, so the rotation lands strictly between the two attempts of one Do.
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mu.Lock()
		got = append(got, r.Header.Get(headerAuthorization))
		first := len(got) == 1
		mu.Unlock()
		if first {
			if err := os.WriteFile(path, []byte("tok-v2"), 0o600); err != nil {
				t.Errorf("rotating the token file: %v", err)
			}
			clk.advance(bearerTestInterval)
			w.WriteHeader(nethttp.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(nethttp.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c, err := bearerBuilder(quietLogger(), path, BearerTokenFileOptions{RefreshInterval: bearerTestInterval}, clk).
		WithRetries(1, time.Millisecond).
		Build()
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), &Request{URL: srv.URL})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"Bearer tok-v1", "Bearer tok-v2"}, got, "the retry must carry the token rotated after the first attempt")
}

func TestBearerTokenFileConcurrentRefreshReadsOnce(t *testing.T) {
	const workers = 32
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-v1"))
	srv, seen := authServer(t)
	clk := newFakeClock()

	var reads atomic.Int32
	release := make(chan struct{})
	b := bearerBuilder(quietLogger(), path, BearerTokenFileOptions{RefreshInterval: bearerTestInterval}, clk)
	b.bearer.readFile = func(p string) ([]byte, error) {
		if reads.Add(1) > 1 {
			<-release
		}
		return readBearerTokenFile(p)
	}
	c, err := b.Build()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("tok-v2"), 0o600))
	clk.advance(bearerTestInterval)

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			_, getErr := c.Get(context.Background(), &Request{URL: srv.URL})
			assert.NoError(t, getErr)
		})
	}
	// One worker holds the lock inside the read; every other one must be parked
	// on it before the read may finish.
	require.Eventually(t, func() bool {
		return testutil.ParkedInSelect("httpclient.(*bearerTokenFile).current(") == workers-1
	}, 5*time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(2), reads.Load(), "one read at Build plus exactly one refresh")
	got := seen()
	require.Len(t, got, workers)
	for _, h := range got {
		assert.Equal(t, "Bearer tok-v2", h)
	}
}

const bearerWaiterFrame = "httpclient.(*bearerTokenFile).acquire("

// holdBearerLock builds a client on a token file and takes its refresh lock, as a
// re-read in flight would; release frees it.
func holdBearerLock(t *testing.T, b *Builder) (c Client, srvURL string, release func()) {
	t.Helper()
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-v1"))
	srv, _ := authServer(t)
	c, err := b.WithBearerTokenFile(path, BearerTokenFileOptions{}).Build()
	require.NoError(t, err)
	s := c.(*client).bearer
	s.lock <- struct{}{}
	release = sync.OnceFunc(func() { <-s.lock })
	t.Cleanup(release)
	return c, srv.URL, release
}

func TestBearerTokenFileRefreshWaitHonorsRequestContext(t *testing.T) {
	c, url, _ := holdBearerLock(t, NewBuilder(quietLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, &Request{URL: url})
		done <- err
	}()
	require.Eventually(t, func() bool { return testutil.ParkedInSelect(bearerWaiterFrame) == 1 }, 5*time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), "bearer token file: waiting for the token")
	case <-time.After(5 * time.Second):
		t.Fatal("a request waiting on the refresh must give up at its own context")
	}
}

func TestBearerTokenFileRefreshWaitIsBoundedByClientTimeout(t *testing.T) {
	const limit = 50 * time.Millisecond
	tests := []struct {
		name    string
		builder func() *Builder
	}{
		{name: "builder_timeout", builder: func() *Builder { return NewBuilder(quietLogger()).WithTimeout(limit) }},
		{name: "http_client_timeout", builder: func() *Builder {
			return NewBuilder(quietLogger()).WithTimeout(0).WithHTTPClient(&nethttp.Client{Timeout: limit})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, url, _ := holdBearerLock(t, tt.builder())

			start := time.Now()
			done := make(chan error, 1)
			go func() {
				_, err := c.Get(context.Background(), &Request{URL: url})
				done <- err
			}()
			select {
			case err := <-done:
				require.Error(t, err)
				assert.True(t, IsErrorType(err, TimeoutError), "got %v", err)
				assert.Contains(t, err.Error(), "timed out waiting for the token")
				assert.GreaterOrEqual(t, time.Since(start), limit)
			case <-time.After(5 * time.Second):
				t.Fatal("the wait for the token must end at the client Timeout")
			}
		})
	}
}

func TestBearerTokenFileRefreshWaitWithoutTimeoutWaitsForTheLock(t *testing.T) {
	c, url, release := holdBearerLock(t, NewBuilder(quietLogger()).WithTimeout(0))

	done := make(chan error, 1)
	go func() {
		_, err := c.Get(context.Background(), &Request{URL: url})
		done <- err
	}()
	require.Eventually(t, func() bool { return testutil.ParkedInSelect(bearerWaiterFrame) == 1 }, 5*time.Second, time.Millisecond)
	release()
	require.NoError(t, <-done)
}

// blockingWarnSink blocks the write of the refresh-failure WARN until released.
type blockingWarnSink struct {
	entered chan struct{}
	release chan struct{}
}

func (w *blockingWarnSink) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("refresh failed")) {
		close(w.entered)
		<-w.release
	}
	return len(p), nil
}

func TestBearerTokenFileWarnsAfterReleasingTheLock(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-v1"))
	srv, _ := authServer(t)
	clk := newFakeClock()
	sink := &blockingWarnSink{entered: make(chan struct{}), release: make(chan struct{})}
	log := logger.New("warn", false).WithContext(zerolog.New(sink).WithContext(context.Background()))
	c, err := bearerBuilder(log, path, BearerTokenFileOptions{RefreshInterval: bearerTestInterval}, clk).Build()
	require.NoError(t, err)

	require.NoError(t, os.Remove(path))
	clk.advance(bearerTestInterval)
	failed := make(chan error, 1)
	go func() {
		_, getErr := c.Get(context.Background(), &Request{URL: srv.URL})
		failed <- getErr
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the failed refresh never logged")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.Get(ctx, &Request{URL: srv.URL})
	close(sink.release)
	require.NoError(t, err, "a request must not wait for another request's WARN")
	require.NoError(t, <-failed)
}

func TestBearerTokenFileFreeLockIgnoresDoneContext(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-v1"))
	c, err := NewBuilder(quietLogger()).WithBearerTokenFile(path, BearerTokenFileOptions{}).Build()
	require.NoError(t, err)
	s := c.(*client).bearer

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// select picks among ready cases at random; repeating makes a lost order visible.
	for range 32 {
		header, getErr := s.current(ctx)
		require.NoError(t, getErr, "a free lock must be taken even under a done context")
		require.Equal(t, "Bearer tok-v1", header)
	}
}

func TestBearerTokenFileFailedRefreshKeepsCachedToken(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(t *testing.T, path string)
	}{
		{name: "file_removed", spoil: func(t *testing.T, path string) { require.NoError(t, os.Remove(path)) }},
		{name: "file_emptied", spoil: func(t *testing.T, path string) { require.NoError(t, os.WriteFile(path, []byte("\n"), 0o600)) }},
		{name: "file_poisoned", spoil: func(t *testing.T, path string) { require.NoError(t, os.WriteFile(path, []byte("tok-a\ntok-b"), 0o600)) }},
		{name: "file_oversized", spoil: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("a"), maxBearerTokenFileBytes+1), 0o600))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTestFile(t, t.TempDir(), "token", []byte("tok-cached"))
			srv, seen := authServer(t)
			clk := newFakeClock()
			log := &fakeLogger{}
			c, err := bearerBuilder(log, path, BearerTokenFileOptions{RefreshInterval: bearerTestInterval}, clk).Build()
			require.NoError(t, err)

			tt.spoil(t, path)
			clk.advance(bearerTestInterval)
			for range 3 {
				mustGet(t, c, srv.URL)
			}

			assert.Equal(t, []string{"Bearer tok-cached", "Bearer tok-cached", "Bearer tok-cached"}, seen())
			warns := log.eventsByLevel("warn")
			require.Len(t, warns, 1, "one WARN per failed refresh, not one per request")
			warnErr, ok := warns[0].fields["error"].(error)
			require.True(t, ok, "the WARN must carry the refresh error")
			assert.Contains(t, warnErr.Error(), strconv.Quote(path))
			assert.NotContains(t, warnErr.Error(), "tok-cached")

			require.NoError(t, os.WriteFile(path, []byte("tok-healed"), 0o600))
			clk.advance(bearerTestInterval)
			mustGet(t, c, srv.URL)
			assert.Equal(t, "Bearer tok-healed", seen()[3])
			assert.Len(t, log.eventsByLevel("warn"), 1)
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
// retry, an error response and a refresh that refuses the file's new contents.
func TestBearerTokenFileNeverLeaksToken(t *testing.T) {
	const token = "leakprobe-7f3c9e1d2b"
	// The refused new contents, split by a control byte; the log escapes that
	// byte, so each side is asserted on its own.
	const rotatedHead, rotatedTail = "rotprobe-quwz", "zkxy-tail"
	tp, cleanup := setupTestTracerForClient(t)
	defer cleanup()

	sink := &syncBuffer{}
	sinkCtx := zerolog.New(sink).WithContext(context.Background())
	log := logger.New("debug", false).WithContext(sinkCtx)

	path := writeTestFile(t, t.TempDir(), "token", []byte(token+"\n"))
	srv, seen := authServer(t, nethttp.StatusInternalServerError, nethttp.StatusOK, nethttp.StatusUnauthorized)
	clk := newFakeClock()
	c, err := bearerBuilder(log, path, BearerTokenFileOptions{RefreshInterval: bearerTestInterval}, clk).
		WithLogPayloads(true).
		WithRetries(1, time.Millisecond).
		Build()
	require.NoError(t, err)

	req := &Request{URL: srv.URL, Headers: map[string]string{"X-Probe": "visible-control"}, Body: []byte(`{"a":1}`)}
	_, err = c.Post(context.Background(), req)
	require.NoError(t, err)
	_, callErr := c.Post(context.Background(), req)
	require.Error(t, callErr)

	require.NoError(t, os.WriteFile(path, []byte(rotatedHead+"\x01"+rotatedTail+"\n"), 0o600))
	clk.advance(bearerTestInterval)
	_, err = c.Post(context.Background(), req)
	require.NoError(t, err)

	require.Len(t, seen(), 4)
	for _, h := range seen() {
		require.Equal(t, "Bearer "+token, h, "the token must actually have been sent for this test to mean anything")
	}

	out := sink.String()
	assert.NotContains(t, out, token)
	assert.NotContains(t, callErr.Error(), token)
	assert.Contains(t, out, "bearer token file refresh failed", "the refresh WARN must have reached the sink")
	assert.Contains(t, out, "holds a byte other than visible ASCII", "the refresh must have read and refused the new contents")
	for _, secret := range []string{rotatedHead, rotatedTail} {
		assert.NotContains(t, out, secret, "the refused new contents must not reach the WARN")
	}

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
	assert.Equal(t, 4, maskedHeaderLines, "one debug header line per attempt")

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
