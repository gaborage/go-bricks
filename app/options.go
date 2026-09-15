package app

import (
	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// Options contains optional dependencies for creating an App instance
type Options struct {
	SignalHandler          SignalHandler
	TimeoutProvider        TimeoutProvider
	Server                 ServerRunner
	ConfigLoader           func() (*config.Config, error)
	DatabaseConnector      func(*config.DatabaseConfig, logger.Logger) (database.Interface, error)
	MessagingClientFactory func(string, logger.Logger) messaging.AMQPClient
	CacheConnector         cache.Connector
	ResourceSource         TenantStore

	// LoggerFilterConfig fully replaces the sensitive-data FilterConfig used by
	// the framework logger. When nil, the framework falls back to
	// config.LogConfig.SensitiveFields (additive to logger.DefaultFilterConfig)
	// or, if that is also empty, to logger.DefaultFilterConfig itself.
	//
	// Use this when you need code-level control beyond a field-name list, e.g.
	// a custom MaskValue, opting out of every default field, or composing
	// values from a secret manager at startup. To opt out entirely, set
	// &logger.FilterConfig{SensitiveFields: nil}. Doing so now emits a WARN at
	// logger construction (suppressed when log.level is above warn).
	//
	// To extend the defaults from code, call logger.DefaultFilterConfig()
	// and append your custom names to SensitiveFields.
	LoggerFilterConfig *logger.FilterConfig

	// PostRegisterRoutes, when set, is called once per Run with every route this App
	// registered — module routes, debug endpoints, and the health/ready probes (one
	// descriptor per method) — after the duplicate-route check and before the listener
	// opens. A non-nil error aborts startup. ModuleName on each descriptor is the
	// registering module's Name(), unless the route set its own with server.WithModule,
	// and is empty for routes the framework registers itself. With an injected Server the
	// slice carries no probe descriptors. Nil means no hook.
	PostRegisterRoutes func(routes []server.RouteDescriptor) error
}
