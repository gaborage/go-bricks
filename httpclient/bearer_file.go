package httpclient

import (
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gaborage/go-bricks/internal/secretfile"
	"github.com/gaborage/go-bricks/logger"
)

// DefaultBearerTokenRefreshInterval is the RefreshInterval a zero
// BearerTokenFileOptions gets — the period client-go re-reads a projected
// service-account token on.
const DefaultBearerTokenRefreshInterval = time.Minute

const headerAuthorization = "Authorization"

// maxBearerTokenFileBytes bounds the read; a service-account JWT is a few KiB.
const maxBearerTokenFileBytes = 64 << 10

var (
	errNotRegularFile    = errors.New("not a regular file")
	errTokenFileTooLarge = fmt.Errorf("larger than %d bytes", maxBearerTokenFileBytes)
)

// BearerTokenFileOptions configures Builder.WithBearerTokenFile.
type BearerTokenFileOptions struct {
	// RefreshInterval is how long a token read from the file is served before the
	// next request re-reads it. Zero means DefaultBearerTokenRefreshInterval; a
	// negative value fails Build.
	RefreshInterval time.Duration
}

// bearerFileSpec is what WithBearerTokenFile records; nil means the option is
// unset. The pointer also keeps Builder comparable despite the func test seams.
type bearerFileSpec struct {
	path     string
	opts     BearerTokenFileOptions
	now      func() time.Time
	readFile func(string) ([]byte, error)
}

// WithBearerTokenFile sends "Authorization: Bearer <token>" on every request and
// every retry, reading the token from path — a file that rotates on disk, such as
// a Kubernetes projected service-account token.
//
// Build trims the path, reads the file eagerly, trims surrounding whitespace,
// and fails when the file is missing, unreadable, not a regular file, larger
// than 64 KiB, empty, or holds anything but visible ASCII (a space, a control
// byte or a byte order mark); the error names the path, never the contents. A
// path that looks like a token instead — one starting "eyJ" as a JWT does, or a
// bare name with no directory and no extension — fails Build without being read
// or echoed; write "./token" for a file in the working directory.
// Build also fails when this option is combined with WithBasicAuth or a default
// Authorization header. Unless the client already has a CheckRedirect, Build
// installs one that refuses a redirect from https to http that would carry the
// token. After Build the file is re-read at most once per RefreshInterval, on
// the request path; a failed re-read keeps the last good token and logs one
// WARN per interval. A request that finds a re-read in progress is served the
// last good token; a stalled read holds only the request performing it, which
// waits whatever its deadline, with nothing logged until the read returns. A
// read stalled past the token's own expiry keeps serving the expired token until
// it returns. An Authorization header the request sets itself, through
// Request.Headers or Request.Auth, wins over the file. The last call wins.
func (b *Builder) WithBearerTokenFile(path string, opts BearerTokenFileOptions) *Builder {
	b.bearer = &bearerFileSpec{path: path, opts: opts, now: time.Now, readFile: readBearerTokenFile}
	return b
}

// bearerTokenFile serves the cached header value and re-reads the file once it is due.
type bearerTokenFile struct {
	path     string
	interval time.Duration
	now      func() time.Time
	readFile func(string) ([]byte, error)
	logger   logger.Logger

	mu     sync.Mutex             // only ever tried; guards next
	header atomic.Pointer[string] // "Bearer <token>"
	next   time.Time
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
	interval := spec.opts.RefreshInterval
	if interval < 0 {
		return nil, fmt.Errorf("httpclient: WithBearerTokenFile: RefreshInterval %s is negative", interval)
	}
	if interval == 0 {
		interval = DefaultBearerTokenRefreshInterval
	}

	s := &bearerTokenFile{
		path:     path,
		interval: interval,
		now:      spec.now,
		readFile: spec.readFile,
		logger:   b.logger,
	}
	s.mu.Lock()
	if err := s.refreshAndUnlock(); err != nil {
		return nil, err
	}
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
	data, err := s.readFile(s.path)
	if err != nil {
		return "", fmt.Errorf("httpclient: bearer token file: %w", secretfile.ReadError(s.path, err))
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("httpclient: bearer token file %s is empty", secretfile.SafeRef(s.path))
	}
	if !isVisibleASCII(token) {
		return "", fmt.Errorf("httpclient: bearer token file %s holds a byte other than visible ASCII", secretfile.SafeRef(s.path))
	}
	return token, nil
}

// isVisibleASCII reports whether every byte of v is VCHAR (0x21-0x7E). That is
// stricter than a header value, which also allows spaces, tabs and obs-text,
// and looser than RFC 6750's b64token, which some issued tokens break.
func isVisibleASCII(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < '!' || c > '~' {
			return false
		}
	}
	return true
}

// current returns the header value to send, re-reading the file when it is due.
// Only the request that takes the lock checks and reads; a request that finds it
// held is served the cached header rather than waiting. A failed read is logged
// after the lock is released.
func (s *bearerTokenFile) current() string {
	if !s.mu.TryLock() {
		return *s.header.Load()
	}
	if err := s.refreshAndUnlock(); err != nil {
		s.logger.Warn().Err(err).Msg("httpclient: bearer token file refresh failed; keeping the last good token")
	}
	return *s.header.Load()
}

// refreshAndUnlock runs with mu held and unlocks it. A failed read keeps the
// cached header and returns the read error.
func (s *bearerTokenFile) refreshAndUnlock() error {
	defer s.mu.Unlock()
	now := s.now()
	if now.Before(s.next) {
		return nil
	}
	// Advanced before the read: a failure waits a full interval to retry, rather
	// than re-reading and re-warning on every request until the file heals.
	s.next = now.Add(s.interval)
	token, err := s.read()
	if err != nil {
		return err
	}
	header := "Bearer " + token
	s.header.Store(&header)
	return nil
}

func (s *bearerTokenFile) apply(req *nethttp.Request) {
	if _, set := req.Header[headerAuthorization]; set {
		return
	}
	req.Header.Set(headerAuthorization, s.current())
}
