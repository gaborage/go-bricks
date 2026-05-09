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

// Options configures TenantSource.
type Options struct {
	// BearerToken is sent in the Authorization header when non-empty.
	BearerToken string

	// Client overrides the default HTTP client. When nil, a client with a
	// DefaultTimeout is used.
	Client *stdhttp.Client

	// PageLimit is sent as the ?limit= query parameter on each request.
	// Servers may cap this. When 0 or negative, DefaultPageLimit is used.
	PageLimit int
}

// TenantSource implements migration.TenantLister against an HTTP control-plane API.
type TenantSource struct {
	baseURL     *url.URL
	bearerToken string
	client      *stdhttp.Client
	pageLimit   int
}

// New constructs a TenantSource. baseURL must be parseable; the /tenants
// path is appended automatically.
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

	client := opts.Client
	if client == nil {
		client = &stdhttp.Client{Timeout: DefaultTimeout}
	}

	limit := opts.PageLimit
	if limit <= 0 {
		limit = DefaultPageLimit
	}

	return &TenantSource{
		baseURL:     u,
		bearerToken: opts.BearerToken,
		client:      client,
		pageLimit:   limit,
	}, nil
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
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
		return nil, fmt.Errorf("control-plane envelope missing data field")
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
