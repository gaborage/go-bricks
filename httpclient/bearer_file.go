package httpclient

import (
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"strings"
	"syscall"

	"github.com/gaborage/go-bricks/internal/secretfile"
)

const headerAuthorization = "Authorization"

// maxBearerTokenFileBytes bounds the read; a service-account JWT is a few KiB.
const maxBearerTokenFileBytes = 64 << 10

var (
	errNotRegularFile    = errors.New("not a regular file")
	errTokenFileTooLarge = fmt.Errorf("larger than %d bytes", maxBearerTokenFileBytes)
)

// bearerFileSpec is what WithBearerTokenFile records; nil means the option is unset.
type bearerFileSpec struct {
	path string
}

// WithBearerTokenFile sends "Authorization: Bearer <token>" on every request and
// every retry, reading the token from path once, at Build.
//
// Build reads the file eagerly, trims surrounding whitespace, and fails when it
// is missing, unreadable, not a regular file, larger than 64 KiB, empty, or not
// an RFC 6750 b64token (letters, digits, "-._~+/", then optional trailing "=");
// the error names the path, never the contents. The path is trimmed too. A path that looks like a token instead —
// one starting "eyJ" as a JWT does, or a bare name with no directory and no
// extension — fails Build without being read or echoed; write "./token" for a
// file in the working directory.
// Build also fails when this option is combined with WithBasicAuth or a default
// Authorization header. Unless the client already has a CheckRedirect, Build
// installs one that refuses a redirect from https to http. An Authorization
// header the request sets itself, through Request.Headers or Request.Auth, wins
// over the file. The last call wins.
func (b *Builder) WithBearerTokenFile(path string) *Builder {
	b.bearer = &bearerFileSpec{path: path}
	return b
}

// bearerTokenFile holds the token read from the file at Build.
type bearerTokenFile struct {
	path  string
	token string
}

// newBearerTokenFile validates WithBearerTokenFile's input against the rest of
// the builder and performs the eager read. It returns nil when the option is unset.
func (b *Builder) newBearerTokenFile() (*bearerTokenFile, error) {
	spec := b.bearer
	if spec == nil {
		return nil, nil
	}
	path := strings.TrimSpace(spec.path)
	if path == "" {
		return nil, errors.New("httpclient: WithBearerTokenFile requires a file path")
	}
	// Refused before any read: a read error would quote the value, and here the
	// value may be the token itself.
	if looksLikeToken(path) {
		return nil, errors.New("httpclient: WithBearerTokenFile: the path looks like a token, not a file path; for a bare file name in the working directory, write ./<name>")
	}
	if b.config.BasicAuth != nil {
		return nil, errors.New("httpclient: WithBearerTokenFile cannot be combined with WithBasicAuth: both set the Authorization header")
	}
	for key := range b.config.DefaultHeaders {
		if nethttp.CanonicalHeaderKey(key) == headerAuthorization {
			return nil, errors.New("httpclient: WithBearerTokenFile cannot be combined with a default Authorization header")
		}
	}

	s := &bearerTokenFile{path: path}
	token, err := s.read()
	if err != nil {
		return nil, err
	}
	s.token = token
	return s, nil
}

// looksLikeToken reports whether a WithBearerTokenFile path is more likely a
// token passed in its place: a compact JWS or JWE, whose JSON header encodes to
// "eyJ", or a bare word with no directory and no extension.
func looksLikeToken(path string) bool {
	return strings.HasPrefix(path, "eyJ") || !strings.ContainsAny(path, `/\.`)
}

// readBearerTokenFile reads a regular file of at most maxBearerTokenFileBytes.
// O_NONBLOCK keeps the open of a FIFO from waiting for a writer, and the type is
// checked on the opened descriptor, so no swap of the path can slip between
// check and read. O_NONBLOCK does not change reads of a regular file.
func readBearerTokenFile(path string) ([]byte, error) {
	// #nosec G304 -- the path is deployment configuration, not request input.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegularFile
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBearerTokenFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBearerTokenFileBytes {
		return nil, errTokenFileTooLarge
	}
	return data, nil
}

func (s *bearerTokenFile) read() (string, error) {
	data, err := readBearerTokenFile(s.path)
	if err != nil {
		return "", fmt.Errorf("httpclient: bearer token file: %w", secretfile.ReadError(s.path, err))
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("httpclient: bearer token file %s is empty", secretfile.SafeRef(s.path))
	}
	if !isB64Token(token) {
		return "", fmt.Errorf("httpclient: bearer token file %s does not hold an RFC 6750 bearer token", secretfile.SafeRef(s.path))
	}
	return token, nil
}

// isB64Token reports whether v matches RFC 6750's b64token:
// 1*( ALPHA / DIGIT / "-" / "." / "_" / "~" / "+" / "/" ) *"=".
func isB64Token(v string) bool {
	body := strings.TrimRight(v, "=")
	if body == "" {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("-._~+/", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func (s *bearerTokenFile) apply(req *nethttp.Request) {
	if _, set := req.Header[headerAuthorization]; set {
		return
	}
	req.Header.Set(headerAuthorization, "Bearer "+s.token)
}
