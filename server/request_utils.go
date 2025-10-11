package server

import "github.com/labstack/echo/v4"

// safeGetRequestID safely extracts request ID from response or falls back to request header.
// SAFETY: Response may be nil after timeout or in edge cases, so we check before accessing.
//
// This utility is used across multiple middleware components (rate limiting, IP pre-guard)
// to ensure consistent and safe request ID extraction even in edge cases like timeouts
// where the response object might be nil.
func safeGetRequestID(c echo.Context) string {
	if resp := c.Response(); resp != nil {
		return resp.Header().Get(echo.HeaderXRequestID)
	}
	return c.Request().Header.Get(echo.HeaderXRequestID)
}
