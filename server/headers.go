package server

// HTTP Header Constants
//
// These constants define standard HTTP header names used across the GoBricks framework.
// Headers already provided by Echo (echo.HeaderContentType, echo.HeaderAuthorization,
// echo.HeaderXRequestID, etc.) should be used directly from the echo package.
//
// Usage:
//
//	resp.Header().Set(HeaderXResponseTime, duration.String())
//	clientIP := req.Header.Get(HeaderXRealIP)

const (
	// HeaderXResponseTime is used to report request processing duration.
	// Set by the timing middleware on all responses.
	HeaderXResponseTime = "X-Response-Time"

	// HeaderXRealIP contains the client's real IP address when behind a proxy.
	// Used by rate limiting and IP-based access control.
	HeaderXRealIP = "X-Real-IP"

	// HeaderXForwardedFor contains a comma-separated list of IPs from proxies.
	// The first entry is typically the original client IP.
	HeaderXForwardedFor = "X-Forwarded-For"

	// HeaderXForwardedHost contains the original host requested by the client.
	// Used in multi-tenant routing with proxy support.
	HeaderXForwardedHost = "X-Forwarded-Host"

	// HeaderXTenantID identifies the tenant in multi-tenant requests.
	// Default header name for tenant resolution.
	HeaderXTenantID = "X-Tenant-ID"
)

// Security Headers
//
// These constants define security-related HTTP headers for protection
// against common web vulnerabilities (XSS, clickjacking, MIME sniffing).

const (
	// HeaderXXSSProtection enables XSS filtering in browsers.
	// Value "1; mode=block" enables protection and blocks rendering on detection.
	HeaderXXSSProtection = "X-XSS-Protection"

	// HeaderXContentTypeOptions prevents MIME type sniffing.
	// Value "nosniff" instructs browsers to strictly follow declared content types.
	HeaderXContentTypeOptions = "X-Content-Type-Options"

	// HeaderXFrameOptions controls page framing for clickjacking protection.
	// Values: "DENY", "SAMEORIGIN", or "ALLOW-FROM uri".
	HeaderXFrameOptions = "X-Frame-Options"
)

// CORS Headers
//
// These constants define Cross-Origin Resource Sharing (CORS) headers
// for controlling cross-origin requests and responses.

const (
	// HeaderAccessControlAllowOrigin specifies allowed origins for CORS requests.
	// Value "*" allows all origins, or specific origin like "https://example.com".
	HeaderAccessControlAllowOrigin = "Access-Control-Allow-Origin"

	// HeaderAccessControlAllowMethods lists allowed HTTP methods for CORS.
	// Example: "GET, POST, PUT, DELETE, OPTIONS".
	HeaderAccessControlAllowMethods = "Access-Control-Allow-Methods"

	// HeaderAccessControlAllowHeaders lists allowed request headers for CORS.
	// Example: "Content-Type, Authorization, X-Request-ID".
	HeaderAccessControlAllowHeaders = "Access-Control-Allow-Headers"

	// HeaderAccessControlAllowCredentials indicates if credentials are allowed.
	// Value "true" allows cookies and authorization headers in CORS requests.
	HeaderAccessControlAllowCredentials = "Access-Control-Allow-Credentials"

	// HeaderAccessControlExposeHeaders lists headers accessible to client scripts.
	// Example: "X-Response-Time, X-Request-ID".
	HeaderAccessControlExposeHeaders = "Access-Control-Expose-Headers"

	// HeaderAccessControlMaxAge specifies preflight cache duration in seconds.
	// Example: "86400" (24 hours).
	HeaderAccessControlMaxAge = "Access-Control-Max-Age"

	// HeaderAccessControlRequestMethod indicates the method for preflight requests.
	// Sent by browsers in OPTIONS preflight requests.
	HeaderAccessControlRequestMethod = "Access-Control-Request-Method"

	// HeaderAccessControlRequestHeaders indicates headers for preflight requests.
	// Sent by browsers in OPTIONS preflight requests.
	HeaderAccessControlRequestHeaders = "Access-Control-Request-Headers"
)
