# HTTP Handler Patterns (Deep Dive)

These are advanced HTTP handler patterns built on top of the basic Enhanced Handler Pattern documented in CLAUDE.md. The basics (type-safe handlers, automatic binding/validation, standardized response envelopes) live in CLAUDE.md; this document covers performance tuning via pointer vs value types, Raw Response Mode for Strangler Fig migrations, the `ResultWithMeta` envelope-meta hook, and route template & path parameter access.

## Handler Performance: Pointer vs Value Types

The enhanced handler pattern supports both **value** and **pointer** types for requests and responses, allowing you to optimize for performance when handling large payloads.

**When to Use Value Types (Default)**:
- ✅ Small requests/responses (<1KB, ~10-15 simple fields)
- ✅ No large embedded arrays or slices
- ✅ Emphasizes immutability (idiomatic Go)
- ✅ Examples: login credentials, ID lookups, simple CRUD operations

**When to Use Pointer Types**:
- ✅ Large requests/responses (>1KB)
- ✅ File uploads (base64-encoded images, documents)
- ✅ Bulk imports/exports (hundreds or thousands of records)
- ✅ Embedded byte arrays or large slices
- ✅ Performance-critical high-traffic endpoints

**Examples**:

```go
// Small request - use value type (default)
type LoginRequest struct {
    Email    string `json:"email" validate:"email"`
    Password string `json:"password" validate:"required"`
}

func (h *Handler) login(req LoginRequest, ctx server.HandlerContext) (server.Result[Token], server.IAPIError) {
    token := h.authService.Authenticate(req)
    return server.NewResult(http.StatusOK, token), nil
}

// Large request - use pointer type for performance
type FileUploadRequest struct {
    Data     []byte `json:"data"` // Base64-encoded file (could be MB)
    Filename string `json:"filename" validate:"required"`
    MimeType string `json:"mime_type"`
}

func (h *Handler) uploadFile(req *FileUploadRequest, ctx server.HandlerContext) (server.Result[UploadResponse], server.IAPIError) {
    // Pointer avoids copying large byte slice
    fileID := h.storageService.Store(req.Data, req.Filename)
    return server.Created(UploadResponse{FileID: fileID}), nil
}

// Large response - use pointer type
type BulkExportResponse struct {
    Records []Record `json:"records"` // Thousands of records
    Total   int      `json:"total"`
}

func (h *Handler) exportAll(req ExportRequest, ctx server.HandlerContext) (*BulkExportResponse, server.IAPIError) {
    records := h.recordService.GetAll(ctx)
    return &BulkExportResponse{
        Records: records,
        Total:   len(records),
    }, nil
}

// Mixed: pointer request, value response
func (h *Handler) processBulk(req *BulkRequest, ctx server.HandlerContext) (Summary, server.IAPIError) {
    summary := h.processor.Process(req)
    return summary, nil
}
```

**Performance Impact**:
- **Value types**: Small struct copy overhead (~nanoseconds for <1KB)
- **Pointer types**: Zero copy overhead, just 8-byte pointer
- **Rule of thumb**: Use pointers when struct size >1KB or contains large slices/arrays

**Linter Configuration**:
Configure `govet` to warn on large value copies:
```yaml
# .golangci.yml  (add under the existing linters: block)
linters:
  settings:
    govet:
      enable:
        - copylocks
        - composites
```

## Raw Response Mode (Strangler Fig Migration)

For **Strangler Fig pattern** migrations — incrementally replacing a legacy API — some routes must return the exact legacy JSON format without the standard `APIResponse` envelope (`data`/`meta` wrapper). Use `WithRawResponse()` for per-route control:

```go
// Legacy-compatible route — no envelope, returns handler response directly as JSON
server.GET(hr, e, "/v1/legacy/users/:id", h.getLegacyUser,
    server.WithRawResponse(),
    server.WithTags("legacy"),
)

// New route — standard APIResponse envelope (unchanged)
server.GET(hr, e, "/v2/users/:id", h.getUser)
```

**Handler returns the exact legacy shape:**
```go
type LegacyUser struct {
    UserID   int64  `json:"userId"`
    UserName string `json:"userName"`
}

func (h *Handler) getLegacyUser(req GetReq, ctx server.HandlerContext) (LegacyUser, server.IAPIError) {
    user, err := h.svc.Find(ctx.RequestContext(), req.ID)
    if err != nil {
        return LegacyUser{}, server.NewNotFoundError("user")
    }
    return LegacyUser{UserID: user.ID, UserName: user.Name}, nil
}
// Response: {"userId": 123, "userName": "Alice"} (no data/meta wrapper)
```

**Error Handling in Raw Mode:**

| Error Path | Raw Mode Behavior |
|------------|-------------------|
| Handler returns `IAPIError` | Minimal JSON: `{"code": "...", "message": "..."}` |
| Binding/validation fails | Same minimal JSON |
| Unhandled error (panic, timeout) | Detected via context key, same minimal JSON |

For **full control** over legacy error formats, catch domain errors in the handler and return them as the response type `R` with a custom status via `Result[R]`. `IAPIError` is for framework-level issues only.

**What is preserved in raw mode:** W3C `traceparent` header propagation, custom headers via `Result[R]`, `NoContent()` (204), all status codes via `Result[R]`.

**What is bypassed:** `data`/`meta` envelope, `APIErrorResponse` envelope (replaced with minimal `{"code","message"}` structure).

## Custom Envelope Meta (`ResultWithMeta[R]`)

The standard `Result[R]` lets handlers control status code and headers, but the response envelope's `meta` map is fixed to `{timestamp, traceId}`. When a handler needs to contribute additional `meta` entries — pagination totals, cursors, rate-limit headroom, deprecation notices — return `server.ResultWithMeta[R]` instead.

```go
type ListUsersResp struct {
    Items []User `json:"items"`
}

func (h *Handler) listUsers(req ListUsersReq, hctx server.HandlerContext) (server.ResultWithMeta[ListUsersResp], server.IAPIError) {
    ctx := hctx.RequestContext()
    users, total, err := h.svc.List(ctx, req.Limit, req.Offset)
    if err != nil {
        return server.ResultWithMeta[ListUsersResp]{}, server.NewInternalServerError("failed to list users")
    }
    return server.ResultWithMeta[ListUsersResp]{
        Data:   ListUsersResp{Items: users},
        Status: http.StatusOK,
        Meta: map[string]any{
            "total":   total,
            "limit":   req.Limit,
            "offset":  req.Offset,
            "hasMore": req.Offset+len(users) < total,
        },
    }, nil
}
```

Produces:

```json
{
  "data": { "items": [...] },
  "meta": {
    "total":     123,
    "limit":     50,
    "offset":    0,
    "hasMore":   true,
    "timestamp": "2026-05-23T12:34:56Z",
    "traceId":   "<uuid>"
  }
}
```

### Reserved Keys

`timestamp` and `traceId` are framework-managed — observability/SRE tooling expects a single authoritative source for both. If a handler supplies either key in its `Meta` map, the framework drops the handler value and emits a structured WARN log identifying the offending key and the route path. The merge is deterministic; reserved keys always win.

### Interaction with Other Modes

| Mode | Behavior |
|------|----------|
| **Standard envelope** (default) | Response body: `{"data": ..., "meta": {handler-meta ∪ framework-meta}}` |
| **Raw mode** (`WithRawResponse()`) | Meta map is silently dropped; only `data` is serialized. A debug log notes the misconfiguration. |
| **JOSE-protected route** | Sealed body is the full `{data, meta}` envelope (symmetric with how the JOSE error path already builds an envelope). Vanilla `Result[R]` from JOSE routes continues to seal bare data unchanged. |

### Compatibility

`ResultWithMeta[R]` implements both `ResultEnvelopeProvider` (preferred by the framework dispatcher) and `ResultMetaProvider` (so third-party middleware that type-asserts the older interface keeps working — `Meta` is dropped on that legacy path).

## Route Template & Path Parameters (v0.46.0+)

Three `HandlerContext` accessors expose the matched route and its path parameters without reaching into the engine:

```go
// PathParam is one matched path parameter; slices preserve route-template order.
type PathParam struct {
    Name  string
    Value string
}

func (c HandlerContext) RouteTemplate() string            // registered template incl. base path — NOT the concrete URL
func (c HandlerContext) PathParams() []PathParam          // ordered defensive copy
func (c HandlerContext) SetPathParams(params []PathParam) // replaces the set; input is copied, nil clears
```

**`RouteTemplate()` — low-cardinality route identity.** Returns the registered route path that matched the request, including any group/base-path prefix (e.g. `/api/cards/:cardId/status`). It is the template the application registered, not the concrete URL — use `ctx.Request().URL.Path` for that. This makes it the right key for per-route registries and metrics: one entry per route regardless of parameter values.

```go
// Per-route latency histograms keyed by route template — low cardinality:
// one entry for "/api/cards/:cardId/status" no matter how many cardIds pass through.
func RouteMetricsMiddleware(byRoute map[string]metric.Float64Histogram) server.MiddlewareFunc {
    return func(c server.HandlerContext, next func() error) error {
        start := time.Now()
        err := next()
        if hist, ok := byRoute[c.RouteTemplate()]; ok { // routes without a histogram are skipped
            hist.Record(c.RequestContext(), time.Since(start).Seconds())
        }
        return err
    }
}
```

**Caveat — matched routes only.** Consumer middleware registered through `RouteRegistrar.Use` (or per-route) executes only when a route matched, so `RouteTemplate()` is always non-empty inside a `server.MiddlewareFunc` — 404/405 traffic never reaches it and cannot be counted or rate-limited there. The empty-template states exist only outside a matched request: unrouted test contexts (`NewHandlerContextForTest`) and engine-level middleware installed via the `SetupMiddlewares(*echo.Echo)` escape hatch, which does run on unmatched requests (on 405 the engine sets the best-matching route's template — engine-defined, not a contract — and `PathParams()` is always empty). Observing unmatched traffic is engine-level territory, not `server.MiddlewareFunc`.

**`PathParams()` — ordered values for substitution.** Returns the matched parameters in route-template order. The returned slice is a defensive copy: safe to retain past the request; mutating it does not affect `Param()` or struct-tag binding.

```go
// Ordered substitution: /api/cards/:cardId/tx/:txId → cache key
// "/api/cards/:cardId/tx/:txId|4111|87". The template namespaces the key per
// route (two routes with equal values cannot collide), and url.PathEscape keeps
// the join injective: "|" and "%" are always escaped, so a crafted value like
// "a|b" cannot fabricate another resource's key. A bare strings.Join of raw
// values would collide — ":" and most delimiters are legal, unencoded path
// characters.
func cacheKey(c server.HandlerContext) string {
    params := c.PathParams()
    parts := make([]string, 0, len(params)+1)
    parts = append(parts, c.RouteTemplate())
    for _, p := range params {
        parts = append(parts, url.PathEscape(p.Value)) // route-template order, always cardId then txId
    }
    return strings.Join(parts, "|")
}
```

**`SetPathParams()` — parameter injection.** Replaces the request's path parameters; subsequent `Param(name)` calls AND `param:"name"` struct-tag binding observe the new set. Typical use: promote a query parameter to a path parameter so legacy clients hit the same downstream handler:

```go
// Promote ?cardId=... to the :cardId path param for legacy clients.
func PromoteCardID(c server.HandlerContext, next func() error) error {
    if c.Param("cardId") == "" {
        if id := c.Query("cardId"); id != "" {
            // Replace an existing entry rather than appending a duplicate: a
            // param can match empty (e.g. /cards//status), and Param() is
            // first-match-wins — an appended duplicate would never be seen.
            params := c.PathParams()
            replaced := false
            for i := range params {
                if params[i].Name == "cardId" {
                    params[i].Value = id
                    replaced = true
                    break
                }
            }
            if !replaced {
                params = append(params, server.PathParam{Name: "cardId", Value: id})
            }
            c.SetPathParams(params)
        }
    }
    return next()
}
```

**Trust caveat:** injected params pass through verbatim — no duplicate- or empty-name validation — and reach `param:"x"` binding in downstream handlers. Treat them with the same trust as their source value: a promoted query param is still user input, so keep the usual `validate:` tags on the bound request struct.
