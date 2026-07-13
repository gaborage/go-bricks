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
//     (development/dev/local) AND CORS_DEV_WILDCARD=true: permissive
//     wildcard for dev convenience (explicit opt-in, ADR-038).
//   - Otherwise (production, staging, custom envs like production-eu, or a
//     development alias without the opt-in): fail closed — no
//     Access-Control-Allow-Origin header is emitted, so browsers reject
//     cross-origin requests. A WARN is logged at startup.
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
				log.Printf("WARN [server.cors] CORS_ORIGINS contains '*' which is forbidden alongside " +
					"AllowCredentials=true; the wildcard entry is being dropped. Use APP_ENV=local/dev/development " +
					"with CORS_DEV_WILDCARD=true for wildcard echo behavior.")
				continue
			}
			parts = append(parts, trimmed)
		}
		if len(parts) == 0 {
			// CORS_ORIGINS was set to commas/whitespace/only-'*' — treat
			// it as the fail-closed case rather than panicking echo.
			emitFailClosedWarn(appEnv)
			failClosed(&cfg)
			return middleware.CORSWithConfig(cfg)
		}
		cfg.AllowOrigins = parts
	case config.IsDevelopment(appEnv):
		if !devWildcardOptIn() {
			emitDevOptInRequiredWarn(appEnv)
			failClosed(&cfg)
			break
		}
		// Echo v5 forbids AllowOrigins=["*"] with AllowCredentials=true,
		// so we keep credentials on for dev convenience and use the unsafe
		// echo-back func to satisfy Echo's validation.
		emitDevPermissiveWarn(appEnv)
		cfg.AllowOrigins = []string{"*"}
		cfg.UnsafeAllowOriginFunc = func(_ *echo.Context, origin string) (string, bool, error) {
			return origin, true, nil
		}
	default:
		// Non-dev env with no explicit CORS_ORIGINS: fail closed.
		emitFailClosedWarn(appEnv)
		emitDevWildcardIgnoredWarn(appEnv)
		failClosed(&cfg)
	}

	return middleware.CORSWithConfig(cfg)
}

// failClosed configures cfg so no Access-Control-Allow-Origin is ever
// emitted: browsers reject cross-origin requests, and credentials are
// dropped so a response can never carry cookies cross-origin. Echo's
// CORSWithConfig panics when AllowOrigins is empty AND
// UnsafeAllowOriginFunc is nil, so the reject-all func doubles as the
// validator-satisfying stand-in.
func failClosed(cfg *middleware.CORSConfig) {
	cfg.AllowCredentials = false
	cfg.UnsafeAllowOriginFunc = func(_ *echo.Context, _ string) (string, bool, error) {
		return "", false, nil
	}
}

// devWildcardOptIn reports whether the operator explicitly granted the
// reflect-any-origin + credentials dev posture via CORS_DEV_WILDCARD.
// Read from the raw process env like CORS_ORIGINS (CORS is configured
// before the framework config/logger are wired). Unparseable values are
// treated as false — fail closed, but loudly (ADR-038).
func devWildcardOptIn() bool {
	raw, ok := os.LookupEnv("CORS_DEV_WILDCARD")
	if !ok || raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("WARN [server.cors] CORS_DEV_WILDCARD=%s is not a valid boolean; "+
			"treating it as false (CORS fails closed).", strconv.Quote(raw))
		return false
	}
	return v
}

// emitDevOptInRequiredWarn explains the fail-closed outcome on a
// development-alias env that has not opted in to the wildcard posture.
func emitDevOptInRequiredWarn(appEnv string) {
	log.Printf("WARN [server.cors] APP_ENV=%s is a development alias but "+
		"CORS_DEV_WILDCARD is not enabled; CORS is failing closed (no "+
		"Access-Control-Allow-Origin emitted). For browser-based local dev set "+
		"CORS_DEV_WILDCARD=true, or set CORS_ORIGINS=http://localhost:3000 for "+
		"a strict allowlist (ADR-038).", strconv.Quote(appEnv))
}

// emitDevWildcardIgnoredWarn surfaces a set-but-ignored CORS_DEV_WILDCARD on a
// non-development env, so operators aren't confused about why the flag "does
// nothing" there. No-op when the flag is unset.
func emitDevWildcardIgnoredWarn(appEnv string) {
	if _, ok := os.LookupEnv("CORS_DEV_WILDCARD"); !ok {
		return
	}
	log.Printf("WARN [server.cors] CORS_DEV_WILDCARD is set but APP_ENV=%s "+
		"is not a development alias; the flag is ignored outside development.",
		strconv.Quote(appEnv))
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

// emitDevPermissiveWarn surfaces the reflect-any-origin + AllowCredentials CORS
// posture loudly. It checks the process environment directly: on the production
// path CORS() receives cfg.App.Env, which koanf has already defaulted to
// "development" when APP_ENV is unset — the argument alone cannot distinguish
// "operator chose dev" from "operator forgot to set APP_ENV".
func emitDevPermissiveWarn(appEnv string) {
	source := "APP_ENV=" + strconv.Quote(appEnv)
	if raw, ok := os.LookupEnv("APP_ENV"); !ok {
		source = "APP_ENV is not set in the process environment (the development default, or a config-file app.env, is in effect)"
	} else if raw != appEnv {
		// envOverride (koanf-resolved) took precedence over a differing raw
		// process value — surface both so operators aren't misled about the source.
		source += " (process APP_ENV=" + strconv.Quote(raw) + ")"
	}
	log.Printf("WARN [server.cors] %s: CORS reflects ANY origin WITH credentials "+
		"(the most permissive posture). If this is production, set APP_ENV=production "+
		"and CORS_ORIGINS=https://your.app. Ignore only for local development.", source)
}
