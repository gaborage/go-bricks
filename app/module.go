package app

import (
	"context"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Module defines the interface that all application modules must implement.
// It provides hooks for initialization, route registration, messaging setup, and cleanup.
type Module interface {
	Name() string
	Init(deps *ModuleDeps) error
	RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar)
	DeclareMessaging(decls *messaging.Declarations)
	Shutdown() error
}

// ModuleDeps contains the dependencies that are injected into each module.
// It provides access to core services like database, logging, messaging, and observability.
// All modules must use GetDB() and GetMessaging() functions for resource access.
type ModuleDeps struct {
	Logger logger.Logger
	Config *config.Config

	// Tracer provides distributed tracing capabilities.
	// Creates spans for tracking operations across services.
	// This is a no-op tracer if observability is disabled.
	Tracer trace.Tracer

	// MeterProvider provides metrics collection capabilities.
	// Use this to create custom meters for application-specific metrics.
	// This is a no-op provider if observability is disabled.
	MeterProvider metric.MeterProvider

	// GetDB returns a database interface for the current context.
	// In single-tenant mode, returns the global database instance.
	// In multi-tenant mode, resolves tenant from context and returns tenant-specific database.
	GetDB func(_ context.Context) (database.Interface, error)

	// GetMessaging returns a messaging client for the current context.
	// In single-tenant mode, returns the global messaging client.
	// In multi-tenant mode, resolves tenant from context and returns tenant-specific client.
	GetMessaging func(_ context.Context) (messaging.AMQPClient, error)
}
