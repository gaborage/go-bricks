package server

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/gaborage/go-bricks/config"
)

// CORS returns a CORS middleware configured for the application.
//
// Origin policy (fail-closed by default per ADR-022 alias semantics):
//   - CORS_ORIGINS set: strict allowlist mode, regardless of env.
//   - CORS_ORIGINS unset AND env is a development alias
//     (development/dev/local): permissive wildcard for dev convenience.
//   - Otherwise (production, staging, custom envs like production-eu):
//     fail closed — no Access-Control-Allow-Origin header is emitted, so
//     browsers reject cross-origin requests. A WARN is logged at startup.
//
// The env can be passed explicitly via the variadic envOverride argument
// (preferred — SetupMiddlewares passes cfg.App.Env, which honors the
// Koanf default of EnvDevelopment when APP_ENV is unset). When omitted,
// CORS falls back to os.Getenv("APP_ENV") — preserving call sites that
// existed before the parameter was added.
//
// exposeResponseTime advertises X-Response-Time in Access-Control-Expose-Headers
// only when the Timing middleware actually emits the header (server.responsetime.
// enabled). Advertising an expose-header the server never sends is harmless but
// misleading, so this keeps the CORS contract aligned with what is on the wire.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in corsEcho, which SetupMiddlewares wires directly on
// the default request path (ADR-026, no per-request baton).
func CORS(exposeResponseTime bool, envOverride ...string) MiddlewareFunc {
	return fromEchoMiddleware(corsEcho(exposeResponseTime, envOverride...))
}

// corsEcho is the echo-native CORS middleware constructor. Public callers use
// CORS (echo-free); SetupMiddlewares uses this form to keep the default chain
// baton-free.
func corsEcho(exposeResponseTime bool, envOverride ...string) echo.MiddlewareFunc {
	exposeHeaders := []string{echo.HeaderXRequestID}
	if exposeResponseTime {
		exposeHeaders = append(exposeHeaders, HeaderXResponseTime)
	}

	cfg := middleware.CORSConfig{
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		},
		AllowHeaders: []string{
			echo.HeaderOrigin,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderAuthorization,
			echo.HeaderXRequestID,
		},
		ExposeHeaders:    exposeHeaders,
		AllowCredentials: true,
		MaxAge:           86400,
	}

	// Prefer the explicit env from the caller (Koanf-loaded config); fall
	// back to APP_ENV from the OS env only when no override was supplied.
	var appEnv string
	if len(envOverride) > 0 {
		appEnv = envOverride[0]
	} else {
		appEnv = os.Getenv("APP_ENV")
	}
	origins := os.Getenv("CORS_ORIGINS")

	switch {
	case origins != "":
		// Explicit allowlist — strict mode for any env. Trim whitespace so
		// "https://a.com, https://b.com" works as operators would expect.
		// Filter empty entries (from trailing commas or doubled separators)
		// because echo's validateOrigin panics on "" at construction time,
		// and operators commonly typo trailing commas.
		raw := strings.Split(origins, ",")
		parts := make([]string, 0, len(raw))
		for _, p := range raw {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				continue
			}
			// Reject "*" in the strict branch: it combines with
			// AllowCredentials=true to panic echo's CORS validator.
			// Operators wanting wildcard-with-credentials must set the
			// env to a dev alias and leave CORS_ORIGINS unset.
			if trimmed == "*" {
				log.Printf("WARN [server.cors] CORS_ORIGINS contains '*' which is forbidden alongside AllowCredentials=true; the wildcard entry is being dropped. Use APP_ENV=local/dev/development for wildcard echo behavior.")
				continue
			}
			parts = append(parts, trimmed)
		}
		if len(parts) == 0 {
			// CORS_ORIGINS was set to commas/whitespace/only-'*' — treat
			// it as the fail-closed case rather than panicking echo.
			emitFailClosedWarn(appEnv)
			cfg.AllowCredentials = false
			cfg.UnsafeAllowOriginFunc = func(_ *echo.Context, _ string) (string, bool, error) {
				return "", false, nil
			}
			return middleware.CORSWithConfig(cfg)
		}
		cfg.AllowOrigins = parts
	case config.IsDevelopment(appEnv):
		// Echo v5 forbids AllowOrigins=["*"] with AllowCredentials=true,
		// so we keep credentials on for dev convenience and use the unsafe
		// echo-back func to satisfy Echo's validation.
		cfg.AllowOrigins = []string{"*"}
		cfg.UnsafeAllowOriginFunc = func(_ *echo.Context, origin string) (string, bool, error) {
			return origin, true, nil
		}
	default:
		// Non-dev env with no explicit CORS_ORIGINS: fail closed.
		emitFailClosedWarn(appEnv)
		// Echo's CORSWithConfig refuses to construct a middleware when both
		// AllowOrigins is empty AND UnsafeAllowOriginFunc is nil — it panics
		// at startup. Provide a reject-all func to satisfy the validator
		// while keeping fail-closed semantics: no Access-Control-Allow-Origin
		// is emitted, so browsers reject cross-origin requests.
		cfg.AllowCredentials = false
		cfg.UnsafeAllowOriginFunc = func(_ *echo.Context, _ string) (string, bool, error) {
			return "", false, nil
		}
	}

	return middleware.CORSWithConfig(cfg)
}

// emitFailClosedWarn surfaces the fail-closed CORS state loudly so operators
// notice before users do. The framework logger isn't wired yet at CORS()
// construction time, so we use the stdlib log package (stderr) — matches
// other pre-bootstrap warnings. strconv.Quote sanitizes the env var
// (escapes control chars, quotes everything as a Go string literal) before
// it flows into the log — breaks gosec G706 taint analysis without losing
// the operator-debugging signal.
func emitFailClosedWarn(appEnv string) {
	log.Printf(
		"WARN [server.cors] APP_ENV=%s is not a development alias and "+
			"CORS_ORIGINS is unset or yields no valid origins; cross-origin "+
			"requests will be rejected. Set CORS_ORIGINS=https://your.app "+
			"to enable a strict allowlist.",
		strconv.Quote(appEnv),
	)
}
