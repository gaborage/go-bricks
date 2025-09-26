package app

import (
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// FactoryResolver encapsulates the logic for resolving factory functions
// from Options, providing default implementations when not specified.
type FactoryResolver struct {
	opts *Options
}

// NewFactoryResolver creates a new factory resolver with the given options.
func NewFactoryResolver(opts *Options) *FactoryResolver {
	return &FactoryResolver{
		opts: opts,
	}
}

// DatabaseConnector returns the appropriate database connector function.
// If no custom connector is provided in options, returns the default implementation.
func (f *FactoryResolver) DatabaseConnector() database.Connector {
	if f.opts != nil && f.opts.DatabaseConnector != nil {
		return f.opts.DatabaseConnector
	}
	return database.NewConnection
}

// MessagingClientFactory returns the appropriate messaging client factory function.
// If no custom factory is provided in options, returns a factory that creates AMQPClient instances.
func (f *FactoryResolver) MessagingClientFactory() messaging.ClientFactory {
	if f.opts != nil && f.opts.MessagingClientFactory != nil {
		return func(url string, log logger.Logger) messaging.AMQPClient {
			return f.opts.MessagingClientFactory(url, log)
		}
	}

	// Default factory function that creates AMQPClient instances
	return func(url string, log logger.Logger) messaging.AMQPClient {
		return messaging.NewAMQPClient(url, log)
	}
}

// ResourceSource returns the appropriate tenant resource source.
// If no custom resource source is provided in options, creates one from config.
func (f *FactoryResolver) ResourceSource(cfg *config.Config) TenantResourceSource {
	if f.opts != nil && f.opts.ResourceSource != nil {
		return f.opts.ResourceSource
	}
	return config.NewTenantResourceSource(cfg)
}

// HasCustomFactories returns true if any custom factories are provided in options.
// This can be useful for logging or debugging purposes.
func (f *FactoryResolver) HasCustomFactories() bool {
	if f.opts == nil {
		return false
	}

	return f.opts.DatabaseConnector != nil ||
		f.opts.MessagingClientFactory != nil ||
		f.opts.ResourceSource != nil
}
