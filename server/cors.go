package server

import (
	"net/http"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// CORS returns a CORS middleware configured for the application.
// It allows cross-origin requests with appropriate security headers.
func CORS() echo.MiddlewareFunc {
	allowedOrigins := []string{"*"}
	if os.Getenv("APP_ENV") == "production" {
		origins := os.Getenv("CORS_ORIGINS")
		if origins != "" {
			allowedOrigins = strings.Split(origins, ",")
		}
	}

	return middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: allowedOrigins,
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
			"X-Request-ID",
		},
		ExposeHeaders: []string{
			"X-Request-ID",
			"X-Response-Time",
		},
		AllowCredentials: true,
		MaxAge:           86400,
	})
}
