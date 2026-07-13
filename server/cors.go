package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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
//     Access-Control-Allow-Origin header is emitted, so browsers block
//     cross-origin JavaScript from reading responses (requests still reach
//     handlers; this is not server-side request rejection or CSRF
//     protection). A WARN is logged at startup.
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

// corsEcho is the echo-native CORS middleware constructor without a framework
// logger: startup warnings fall back to the stdlib log package. The public
// CORS constructor uses this form.
func corsEcho(exposeResponseTime bool, envOverride ...string) echo.MiddlewareFunc {
	return corsEchoWithLogger(exposeResponseTime, nil, envOverride...)
}

// corsEchoWithLogger is the echo-native CORS middleware constructor.
// SetupMiddlewares uses this form (keeping the default chain baton-free,
// ADR-026), passing its framework logger so startup warnings flow through
// structured logging (SensitiveDataFilter, dual-mode routing). l may be nil,
// in which case warnings fall back to the stdlib log package via corsWarnf.
func corsEchoWithLogger(exposeResponseTime bool, l logger.Logger, envOverride ...string) echo.MiddlewareFunc {
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
		// Explicit allowlist — strict mode for any env.
		parts := parseAllowedOrigins(l, origins)
		if len(parts) == 0 {
			// CORS_ORIGINS was set to commas/whitespace/only-'*' — treat
			// it as the fail-closed case rather than panicking echo. The
			// warn names the invalid allowlist as the cause, not the env
			// (this path is reachable on a dev alias too).
			corsWarnf(l, "CORS_ORIGINS was set but yielded no valid explicit origins; "+
				"CORS is failing closed (no Access-Control-Allow-Origin emitted). Provide "+
				"at least one explicit origin, e.g. CORS_ORIGINS=https://your.app.")
			failClosed(&cfg)
			return middleware.CORSWithConfig(cfg)
		}
		cfg.AllowOrigins = parts
	case config.IsDevelopment(appEnv):
		if !devWildcardOptIn(l) {
			emitDevOptInRequiredWarn(l, appEnv)
			failClosed(&cfg)
			break
		}
		// Echo v5 forbids AllowOrigins=["*"] with AllowCredentials=true,
		// so we keep credentials on for dev convenience and use the unsafe
		// echo-back func to satisfy Echo's validation.
		emitDevPermissiveWarn(l, appEnv)
		cfg.AllowOrigins = []string{"*"}
		cfg.UnsafeAllowOriginFunc = func(_ *echo.Context, origin string) (string, bool, error) {
			return origin, true, nil
		}
	default:
		// Non-dev env with no explicit CORS_ORIGINS: fail closed.
		emitFailClosedWarn(l, appEnv)
		emitDevWildcardIgnoredWarn(l, appEnv)
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

// parseAllowedOrigins splits the raw CORS_ORIGINS value into a filtered
// strict allowlist. Whitespace is trimmed so "https://a.com, https://b.com"
// works as operators would expect. Empty entries (from trailing commas or
// doubled separators) are skipped because echo's validateOrigin panics on ""
// at construction time, and operators commonly typo trailing commas. "*"
// entries are dropped with a WARN: the wildcard combines with
// AllowCredentials=true to panic echo's CORS validator, and operators wanting
// wildcard-with-credentials must go through the dev opt-in instead (ADR-038).
func parseAllowedOrigins(l logger.Logger, origins string) []string {
	raw := strings.Split(origins, ",")
	parts := make([]string, 0, len(raw))
	for _, p := range raw {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if trimmed == "*" {
			corsWarnf(l, "CORS_ORIGINS contains '*' which is forbidden alongside "+
				"AllowCredentials=true; the wildcard entry is being dropped. Keep only explicit origins in "+
				"CORS_ORIGINS. For wildcard echo behavior instead: unset CORS_ORIGINS, set APP_ENV to a "+
				"development alias (e.g. APP_ENV=development), and set CORS_DEV_WILDCARD=true.")
			continue
		}
		parts = append(parts, trimmed)
	}
	return parts
}

// corsWarnf emits a CORS startup warning. With a framework logger it routes
// through structured WARN-level logging (SensitiveDataFilter, dual-mode
// routing); without one (public CORS()/corsEcho construction, which has no
// logger to thread) it falls back to the stdlib log package. The
// "[server.cors] " prefix is kept on both paths for grep continuity.
func corsWarnf(l logger.Logger, format string, args ...any) {
	if l == nil {
		log.Printf("WARN [server.cors] "+format, args...)
		return
	}
	l.Warn().Msg("[server.cors] " + fmt.Sprintf(format, args...))
}

// devWildcardOptIn reports whether the operator explicitly granted the
// reflect-any-origin + credentials dev posture via CORS_DEV_WILDCARD.
// Read from the raw process env like CORS_ORIGINS (the CORS policy is
// deliberately built from raw process env, not the koanf config — ADR-038
// Option C). Unparseable values are treated as false — fail closed, but
// loudly (ADR-038).
func devWildcardOptIn(l logger.Logger) bool {
	raw, ok := os.LookupEnv("CORS_DEV_WILDCARD")
	if !ok || raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		corsWarnf(l, "CORS_DEV_WILDCARD=%s is not a valid boolean; "+
			"treating it as false (CORS fails closed).", strconv.Quote(raw))
		return false
	}
	return v
}

// emitDevOptInRequiredWarn explains the fail-closed outcome on a
// development-alias env that has not opted in to the wildcard posture.
func emitDevOptInRequiredWarn(l logger.Logger, appEnv string) {
	corsWarnf(l, "APP_ENV=%s is a development alias but "+
		"CORS_DEV_WILDCARD is not enabled; CORS is failing closed (no "+
		"Access-Control-Allow-Origin emitted). For browser-based local dev set "+
		"CORS_DEV_WILDCARD=true, or set CORS_ORIGINS=http://localhost:3000 for "+
		"a strict allowlist (ADR-038).", strconv.Quote(appEnv))
}

// emitDevWildcardIgnoredWarn surfaces a set-but-ignored CORS_DEV_WILDCARD on a
// non-development env, so operators aren't confused about why the flag "does
// nothing" there. No-op when the flag is unset.
func emitDevWildcardIgnoredWarn(l logger.Logger, appEnv string) {
	if _, ok := os.LookupEnv("CORS_DEV_WILDCARD"); !ok {
		return
	}
	corsWarnf(l, "CORS_DEV_WILDCARD is set but APP_ENV=%s "+
		"is not a development alias; the flag is ignored outside development.",
		strconv.Quote(appEnv))
}

// emitFailClosedWarn surfaces the fail-closed CORS state on a non-dev env
// with no CORS_ORIGINS (its only call site is the default branch — the
// invalid-explicit-allowlist path has its own warn naming the allowlist as
// the cause). The framework path (SetupMiddlewares) threads its logger
// through corsEchoWithLogger so this lands in structured logging; stdlib log
// is only the corsWarnf fallback when CORS()/corsEcho is constructed without
// a logger. strconv.Quote sanitizes the env var (escapes control chars,
// quotes everything as a Go string literal) before it flows into the log —
// breaks gosec G706 taint analysis without losing the operator-debugging
// signal.
func emitFailClosedWarn(l logger.Logger, appEnv string) {
	corsWarnf(l,
		"APP_ENV=%s is not a development alias and "+
			"CORS_ORIGINS is unset; cross-origin "+
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
func emitDevPermissiveWarn(l logger.Logger, appEnv string) {
	source := "APP_ENV=" + strconv.Quote(appEnv)
	if raw, ok := os.LookupEnv("APP_ENV"); !ok {
		source = "APP_ENV is not set in the process environment (the development default, or a config-file app.env, is in effect)"
	} else if raw != appEnv {
		// envOverride (koanf-resolved) took precedence over a differing raw
		// process value — surface both so operators aren't misled about the source.
		source += " (process APP_ENV=" + strconv.Quote(raw) + ")"
	}
	corsWarnf(l, "%s: CORS reflects ANY origin WITH credentials "+
		"(the most permissive posture). If this is production, set APP_ENV=production "+
		"and CORS_ORIGINS=https://your.app. Ignore only for local development.", source)
}
