// Package http provides a TenantLister that pulls tenant IDs from a control-plane
// API conforming to the go-bricks pre-defined contract.
//
// Contract (responses use the standard go-bricks APIResponse envelope):
//
//	GET <base>/tenants?limit=<int>&cursor=<opaque>
//	200 OK
//	{
//	  "data":  { "tenants": [ {"id": "..."}, ... ], "next_cursor": "..." },
//	  "meta":  { "timestamp": "...", "traceId": "..." }
//	}
//	4xx/5xx
//	{ "error": { "code": "...", "message": "..." }, "meta": { ... } }
//
// next_cursor empty/absent ends the iteration.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultPageLimit is the default page size requested from the control-plane.
const DefaultPageLimit = 100

// DefaultTimeout is applied to the underlying *http.Client when none is supplied.
const DefaultTimeout = 30 * time.Second

// maxPages bounds ListTenants so a misbehaving server returning a non-progressing
// next_cursor cannot loop forever. At DefaultPageLimit=100, this caps the result
// set at one million tenants — well past any realistic fleet size.
const maxPages = 10000

// maxRedirects mirrors net/http's default redirect cap, which a custom
// CheckRedirect replaces wholesale (see net/http.defaultCheckRedirect).
const maxRedirects = 10

// DefaultMaxResponseBytes caps a single page response when
// Options.MaxResponseBytes is unset. 4 MiB is far past any real page: a page
// carries at most PageLimit tenant IDs, and even 1000 UUIDs is ~40 KB.
// Spelled as a plain literal rather than 4 << 20 so the mutation gate cannot
// generate a bitwise-shift mutant on a line no test could distinguish.
const DefaultMaxResponseBytes int64 = 4194304 // 4 MiB

// Options configures TenantSource.
type Options struct {
	// BearerToken is sent in the Authorization header when non-empty.
	BearerToken string

	// Client overrides the default HTTP client. When nil, a client with a
	// DefaultTimeout is used. A caller-supplied CheckRedirect is honored as-is
	// and replaces the source's scheme/host redirect guard.
	Client *stdhttp.Client

	// PageLimit is sent as the ?limit= query parameter on each request.
	// Servers may cap this. When 0 or negative, DefaultPageLimit is used.
	PageLimit int

	// AllowInsecureScheme opts in to accepting `http://` base URLs. By default
	// New() rejects plaintext schemes so an operator-supplied bearer token is
	// never transmitted in clear text. Set this to true only for LocalStack,
	// dev loops, or internal-only tooling on private networks. It additionally
	// permits redirects that downgrade to `http://`.
	AllowInsecureScheme bool

	// MaxResponseBytes caps how many bytes are read from a single page
	// response before the read fails with ErrResponseTooLarge. When 0 or
	// negative, DefaultMaxResponseBytes is used.
	MaxResponseBytes int64
}

// ErrInsecureScheme is returned by New when the supplied base URL uses a
// plaintext scheme (`http://`) and Options.AllowInsecureScheme is false.
var ErrInsecureScheme = errors.New("migration/source/http: http:// scheme requires Options.AllowInsecureScheme=true (defense-in-depth: bearer token would be transmitted in clear text)")

// ErrUnsupportedScheme is returned by New when the supplied base URL uses a
// scheme other than http or https.
var ErrUnsupportedScheme = errors.New("migration/source/http: unsupported URL scheme (expected http or https)")

// ErrResponseTooLarge is returned when one page response exceeds
// Options.MaxResponseBytes (DefaultMaxResponseBytes when unset).
var ErrResponseTooLarge = errors.New("migration/source/http: control-plane response exceeded the configured maximum")

// TenantSource implements migration.TenantLister against an HTTP control-plane API.
type TenantSource struct {
	baseURL          *url.URL
	bearerToken      string
	client           *stdhttp.Client
	pageLimit        int
	maxResponseBytes int64
}

// New constructs a TenantSource. baseURL must be parseable; the /tenants
// path is appended automatically. Plaintext `http://` URLs are rejected unless
// opts.AllowInsecureScheme is true (defense-in-depth around the bearer token).
func New(baseURL string, opts Options) (*TenantSource, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("migration/source/http: baseURL is empty")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("migration/source/http: parse baseURL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("migration/source/http: baseURL must include scheme and host: %q", baseURL)
	}
	// url.Parse lowercases the scheme per RFC 3986, so a direct equality check
	// is sufficient here.
	switch u.Scheme {
	case "https":
	case "http":
		if !opts.AllowInsecureScheme {
			return nil, ErrInsecureScheme
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedScheme, u.Scheme)
	}

	client := opts.Client
	if client == nil {
		client = &stdhttp.Client{Timeout: DefaultTimeout}
	}
	client = redirectGuard(client, u.Host, opts.AllowInsecureScheme)

	limit := opts.PageLimit
	if limit <= 0 {
		limit = DefaultPageLimit
	}

	maxBytes := opts.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}

	return &TenantSource{
		baseURL:          u,
		bearerToken:      opts.BearerToken,
		client:           client,
		pageLimit:        limit,
		maxResponseBytes: maxBytes,
	}, nil
}

// redirectGuard returns client unchanged when the caller installed its own
// CheckRedirect, and otherwise a shallow copy carrying a policy that refuses
// any redirect which downgrades to http:// or leaves the base host — net/http
// forwards Authorization on both (it compares hosts only, never the scheme).
func redirectGuard(client *stdhttp.Client, baseHost string, allowInsecure bool) *stdhttp.Client {
	if client.CheckRedirect != nil {
		return client
	}
	// Shallow-copy: New must never mutate a client the caller may reuse
	// (same rule as httpclient/client.go:531-539).
	c := *client
	c.CheckRedirect = func(req *stdhttp.Request, via []*stdhttp.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme == "http" && !allowInsecure {
			return fmt.Errorf("%w: redirect to %q downgrades to http", ErrInsecureScheme, req.URL.Redacted())
		}
		if req.URL.Host != baseHost {
			return fmt.Errorf("control-plane redirected off-host to %q; refusing to follow", req.URL.Redacted())
		}
		return nil
	}
	return &c
}

// envelope mirrors the go-bricks server.APIResponse shape (see server/handler.go).
// Defined inline to keep this package a leaf import.
type envelope struct {
	Data  *envelopeData  `json:"data,omitempty"`
	Error *envelopeError `json:"error,omitempty"`
}

type envelopeData struct {
	Tenants    []tenantEntry `json:"tenants"`
	NextCursor string        `json:"next_cursor"`
}

type tenantEntry struct {
	ID string `json:"id"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ContractError is returned when the control-plane responds with a non-2xx
// status. It exposes the envelope's error fields for diagnostics.
type ContractError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ContractError) Error() string {
	if e.Code == "" && e.Message == "" {
		return fmt.Sprintf("control-plane returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("control-plane returned HTTP %d: %s: %s", e.StatusCode, e.Code, e.Message)
}

// ListTenants walks all pages of the contract endpoint and returns the union
// of tenant IDs.
func (s *TenantSource) ListTenants(ctx context.Context) ([]string, error) {
	var (
		out    []string
		cursor string
	)

	seenCursors := make(map[string]struct{})
	for i := 0; i < maxPages; i++ {
		page, err := s.fetchPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		for _, t := range page.Tenants {
			id := strings.TrimSpace(t.ID)
			if id == "" {
				continue
			}
			out = append(out, id)
		}
		if page.NextCursor == "" {
			return out, nil
		}
		if _, dup := seenCursors[page.NextCursor]; dup {
			return nil, fmt.Errorf("control-plane returned repeated next_cursor %q (cycle detected)", page.NextCursor)
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("control-plane pagination exceeded %d pages", maxPages)
}

func (s *TenantSource) fetchPage(ctx context.Context, cursor string) (*envelopeData, error) {
	pageURL := *s.baseURL
	pageURL.Path = strings.TrimRight(pageURL.Path, "/") + "/tenants"

	q := pageURL.Query()
	q.Set("limit", strconv.Itoa(s.pageLimit))
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	pageURL.RawQuery = q.Encode()

	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodGet, pageURL.String(), stdhttp.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if s.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearerToken)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request control-plane: %w", err)
	}
	defer resp.Body.Close()

	// nil ResponseWriter is deliberate — the close signal is gated on an
	// unexported net/http method nothing here can satisfy (same as
	// server/jose.go:316-319 and httpclient/jose_transport.go:217).
	body, err := io.ReadAll(stdhttp.MaxBytesReader(nil, resp.Body, s.maxResponseBytes))
	if err != nil {
		var maxErr *stdhttp.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, fmt.Errorf("%w (%d bytes)", ErrResponseTooLarge, s.maxResponseBytes)
		}
		return nil, fmt.Errorf("read control-plane response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseContractError(resp.StatusCode, body)
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode control-plane envelope: %w", err)
	}
	if env.Data == nil {
		return nil, errors.New("control-plane envelope missing data field")
	}
	return env.Data, nil
}

func parseContractError(status int, body []byte) error {
	out := &ContractError{StatusCode: status}
	if len(body) == 0 {
		return out
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil {
		out.Code = env.Error.Code
		out.Message = env.Error.Message
	}
	return out
}
