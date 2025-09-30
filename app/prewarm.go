package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// ConnectionPreWarmer handles pre-warming of database and messaging connections
// for improved startup performance and health checking.
type ConnectionPreWarmer struct {
	logger           logger.Logger
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
}

// NewConnectionPreWarmer creates a new connection pre-warmer.
func NewConnectionPreWarmer(
	log logger.Logger,
	dbManager *database.DbManager,
	messagingManager *messaging.Manager,
) *ConnectionPreWarmer {
	return &ConnectionPreWarmer{
		logger:           log,
		dbManager:        dbManager,
		messagingManager: messagingManager,
	}
}

// PreWarmSingleTenant pre-warms connections for single-tenant deployments.
// It establishes database connections and messaging consumers/publishers upfront.
// Errors are logged as warnings and don't cause startup failure.
func (w *ConnectionPreWarmer) PreWarmSingleTenant(
	ctx context.Context,
	declarations *messaging.Declarations,
) error {
	var errs []error

	if w.dbManager != nil {
		// Pre-warm database connection
		if err := w.PreWarmDatabase(ctx, ""); err != nil {
			// Check if error is due to database not being configured
			if contains(err.Error(), "database not configured") || contains(err.Error(), "no default database") {
				w.logger.Debug().Msg("Skipping single-tenant database pre-warming: not configured")
			} else {
				w.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant database connection")
				errs = append(errs, fmt.Errorf("database pre-warming failed: %w", err))
			}
		} else {
			w.logger.Info().Msg("Pre-warmed single-tenant database connection")
		}
	} else {
		w.logger.Debug().Msg("Skipping single-tenant database pre-warming: manager unavailable")
	}

	if w.messagingManager != nil {
		// Pre-warm messaging components
		if err := w.PreWarmMessaging(ctx, "", declarations); err != nil {
			// Check if error is due to messaging not being configured
			if contains(err.Error(), "messaging not configured") {
				w.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: not configured")
			} else {
				w.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant messaging")
				errs = append(errs, fmt.Errorf("messaging pre-warming failed: %w", err))
			}
		} else {
			w.logger.Info().Msg("Pre-warmed single-tenant messaging")
		}
	} else {
		w.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: manager unavailable")
	}

	// Return combined errors but don't fail startup
	if len(errs) > 0 {
		return fmt.Errorf("pre-warming issues (non-fatal): %v", errs)
	}

	return nil
}

// PreWarmDatabase attempts to establish a database connection for the given key.
// Returns error but caller determines if it's fatal.
func (w *ConnectionPreWarmer) PreWarmDatabase(ctx context.Context, key string) error {
	if w.dbManager == nil {
		return fmt.Errorf("database manager not available")
	}

	_, err := w.dbManager.Get(ctx, key)
	return err
}

// PreWarmMessaging attempts to establish messaging components for the given key.
// This includes ensuring consumers are set up and getting a publisher.
// Returns error but caller determines if it's fatal.
func (w *ConnectionPreWarmer) PreWarmMessaging(
	ctx context.Context,
	key string,
	declarations *messaging.Declarations,
) error {
	if w.messagingManager == nil {
		return fmt.Errorf("messaging manager not available")
	}

	// Ensure consumers are set up
	if declarations != nil {
		if err := w.messagingManager.EnsureConsumers(ctx, key, declarations); err != nil {
			return fmt.Errorf("failed to ensure consumers: %w", err)
		}
		w.logger.Info().Msg("Ensured messaging consumers")
	}

	// Pre-warm publisher
	if _, err := w.messagingManager.GetPublisher(ctx, key); err != nil {
		return fmt.Errorf("failed to get publisher: %w", err)
	}
	w.logger.Info().Msg("Pre-warmed messaging publisher")

	return nil
}

// IsAvailable returns true if both database and messaging managers are available.
func (w *ConnectionPreWarmer) IsAvailable() bool {
	return w.dbManager != nil || w.messagingManager != nil
}

// LogAvailability logs which components are available for pre-warming.
func (w *ConnectionPreWarmer) LogAvailability() {
	if w.dbManager == nil {
		w.logger.Debug().Msg("Database manager not available for pre-warming")
	} else {
		w.logger.Debug().Msg("Database manager available for pre-warming")
	}

	if w.messagingManager == nil {
		w.logger.Debug().Msg("Messaging manager not available for pre-warming")
	} else {
		w.logger.Debug().Msg("Messaging manager available for pre-warming")
	}
}
