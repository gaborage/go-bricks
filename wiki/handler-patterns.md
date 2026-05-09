# HTTP Handler Patterns (Deep Dive)

These are advanced HTTP handler patterns built on top of the basic Enhanced Handler Pattern documented in CLAUDE.md. The basics (type-safe handlers, automatic binding/validation, standardized response envelopes) live in CLAUDE.md; this document covers performance tuning via pointer vs value types, and Raw Response Mode for Strangler Fig migrations.

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

func (h *Handler) login(req LoginRequest, ctx server.HandlerContext) (Result[Token], server.IAPIError) {
    token := h.authService.Authenticate(req)
    return server.OK(token), nil
}

// Large request - use pointer type for performance
type FileUploadRequest struct {
    Data     []byte `json:"data"` // Base64-encoded file (could be MB)
    Filename string `json:"filename" validate:"required"`
    MimeType string `json:"mime_type"`
}

func (h *Handler) uploadFile(req *FileUploadRequest, ctx server.HandlerContext) (Result[UploadResponse], server.IAPIError) {
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
# .golangci.yml
linters-settings:
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
    user, err := h.svc.Find(ctx.Echo.Request().Context(), req.ID)
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
