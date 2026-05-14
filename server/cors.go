package server

import (
	"net/http"
	"os"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/gaborage/go-bricks/config"
)

// CORS returns a CORS middleware configured for the application.
// It allows cross-origin requests with appropriate security headers.
func CORS() echo.MiddlewareFunc {
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
		ExposeHeaders: []string{
			echo.HeaderXRequestID,
			HeaderXResponseTime,
		},
		AllowCredentials: true,
		MaxAge:           86400,
	}

	// CORS() is constructed before config validation has run, so we read
	// APP_ENV directly rather than going through cfg.App.IsProduction().
	useWildcard := true
	if config.IsProduction(os.Getenv("APP_ENV")) {
		origins := os.Getenv("CORS_ORIGINS")
		if origins != "" {
			cfg.AllowOrigins = strings.Split(origins, ",")
			useWildcard = false
		}
	}

	if useWildcard {
		// Echo v5 does not allow AllowOrigins=["*"] with AllowCredentials=true.
		// Use UnsafeAllowOriginFunc to replicate the previous wildcard behaviour.
		cfg.AllowOrigins = []string{"*"} // Echo v5 CORSConfig validation requires AllowOrigins; actual matching uses UnsafeAllowOriginFunc below
		cfg.UnsafeAllowOriginFunc = func(_ *echo.Context, origin string) (string, bool, error) {
			return origin, true, nil
		}
	}

	return middleware.CORSWithConfig(cfg)
}
