package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// normalizeMultitenant shapes the multitenant section: resolver and limits
// fills, then (static source only) each tenant's database (opaque) and cache.
// Map-key rules (empty/dotted ID, messaging consistency, single-tenant
// conflicts) are check's — see checkMultitenant.
func normalizeMultitenant(mt *MultitenantConfig, source *SourceConfig) error {
	if !mt.Enabled {
		return nil
	}

	normalizeMultitenantResolver(&mt.Resolver)
	normalizeMultitenantLimits(&mt.Limits)

	if hasStaticTenants(source, mt) {
		if err := normalizeMultitenantTenants(mt.Tenants); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}
	}

	return nil
}

// hasStaticTenants is the phase-shared gate for the static tenant map: dynamic
// sources load tenants from an external store and never reach it. A delivered
// but empty static map is not "has tenants" — check rejects that case.
func hasStaticTenants(source *SourceConfig, mt *MultitenantConfig) bool {
	return source.Type == SourceTypeStatic && len(mt.Tenants) > 0
}

// checkMultitenant rejects a normalized multitenant section without changing
// it: resolver and limits enumerations, source type, and (static source only)
// the tenant map's key rules and its conflicts with single-tenant config.
func checkMultitenant(mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig, source *SourceConfig) error {
	if !mt.Enabled {
		return nil
	}

	if err := checkMultitenantResolver(&mt.Resolver); err != nil {
		return fmt.Errorf("resolver: %w", err)
	}

	if err := checkMultitenantLimits(&mt.Limits); err != nil {
		return fmt.Errorf("limits: %w", err)
	}

	if err := validateSourceConfig(source); err != nil {
		return fmt.Errorf("source: %w", err)
	}

	// For static sources, validate tenants if provided (optional but must be valid if present)
	// For dynamic sources, tenants are optional and loaded from external store
	if source.Type == SourceTypeStatic && mt.Tenants != nil {
		if len(mt.Tenants) == 0 {
			return errors.New("tenants: empty map provided - either omit tenants section or provide at least one tenant for static source")
		}

		if err := checkTenantMessagingConsistency(mt.Tenants); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}

		if err := checkTenantMessagingReachable(mt.Tenants, msg); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}

		// Sorted, like forEachDatabaseSection: with several malformed tenants the
		// startup error names the same one every run.
		for _, tenantID := range slices.Sorted(maps.Keys(mt.Tenants)) {
			tenant := mt.Tenants[tenantID]
			if err := checkMultitenantTenantEntry(tenantID, &tenant); err != nil {
				return fmt.Errorf("tenants: %w", err)
			}
		}
	}

	if hasStaticTenants(source, mt) {
		return validateNoSingleTenantConflict(db, msg)
	}

	return nil
}

// checkMultitenantTenantEntry rejects one static tenant's ID and cache
// section: an empty or dotted ID, an ID no environment variable can address
// (checkSectionName), or (once the ID is valid) whatever checkTenantCache
// rejects.
func checkMultitenantTenantEntry(tenantID string, entry *TenantEntry) error {
	if tenantID == "" {
		return NewValidationError(fieldMultitenantTenants, "tenant ID cannot be empty")
	}
	// A '.' collides with koanf's path delimiter: the constructed section path
	// multitenant.tenants.<id>.database becomes ambiguous. Koanf has no
	// delimiter escaping, so fail fast rather than let a later lookup consult
	// the wrong flattened key.
	if strings.Contains(tenantID, ".") {
		return NewValidationError(fieldMultitenantTenants,
			fmt.Sprintf("tenant ID %q cannot contain '.' (the config path delimiter)", tenantID))
	}
	// Static tenant map keys only. A dynamic source never reaches here (see
	// checkMultitenant's SourceTypeStatic gate); the resolver's own grammar is
	// its request-time gate.
	if err := checkSectionName(fmt.Sprintf(tenantsFieldPrefix, tenantID), tenantID); err != nil {
		return err
	}
	return checkTenantCache(tenantID, &entry.Cache)
}

// validateNoSingleTenantConflict checks for conflicts with single-tenant configuration
func validateNoSingleTenantConflict(db *DatabaseConfig, msg *MessagingConfig) error {
	if IsDatabaseConfigured(db) {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabase,
			Message:  "not allowed when static tenants are configured",
			Action:   "remove database section from root config or move to multitenant.tenants.<tenant_id>.database",
		}
	}
	// Under shared tenancy the root block IS the control-plane broker every tenant
	// is served from, so it is the configuration this mode requires, not a conflict.
	if IsMessagingConfigured(msg) && msg.Tenancy != TenancyShared {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldMessaging,
			Message:  "not allowed when static tenants are configured",
			Action:   "remove messaging section from root config or move to multitenant.tenants.<tenant_id>.messaging",
		}
	}
	return nil
}

// normalizeMultitenantLimits fills the tenant-count default. A negative value
// is treated the same as zero — kept intentionally, not tightened to reject.
func normalizeMultitenantLimits(cfg *LimitsConfig) {
	if cfg.Tenants <= 0 {
		cfg.Tenants = 100 // default
	}
}

// checkMultitenantLimits rejects a tenant cap above 1000; zero and negatives
// were already defaulted by normalizeMultitenantLimits.
func checkMultitenantLimits(cfg *LimitsConfig) error {
	if cfg.Tenants > 1000 {
		return NewValidationError("multitenant.limits.tenants", "cannot exceed 1000")
	}
	return nil
}

// normalizeMultitenantTenants shapes each static tenant's database (opaque)
// and cache, and writes the result back to the map. The tenant-ID rules
// (empty, dotted) and cross-tenant messaging consistency are check's — see
// checkMultitenant.
func normalizeMultitenantTenants(tenants map[string]TenantEntry) error {
	// Sorted, like forEachDatabaseSection: with several malformed tenants the
	// startup error names the same one every run.
	for _, tenantID := range slices.Sorted(maps.Keys(tenants)) {
		tenant := tenants[tenantID]

		if err := normalizeDatabaseSection(&tenant.Database, tenantDatabaseSection(tenantID)); err != nil {
			return err
		}

		if err := normalizeTenantCache(&tenant.Cache); err != nil {
			return err
		}

		// Persist defaults back to the map (see normalizeNamedDatabases for rationale).
		tenants[tenantID] = tenant
	}

	return nil
}

// checkTenantMessagingConsistency enforces all-or-nothing messaging configuration
// across tenants: if any tenant has messaging configured, all must have it
// configured. This prevents confusing scenarios where some tenants can use
// messaging and others cannot.
func checkTenantMessagingConsistency(tenants map[string]TenantEntry) error {
	hasAnyMessaging := false
	hasNoMessaging := false

	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if isTenantMessagingConfigured(&tenant.Messaging) {
			hasAnyMessaging = true
		} else {
			hasNoMessaging = true
		}
	}

	if hasAnyMessaging && hasNoMessaging {
		return &ConfigError{
			Category: errCategoryInvalid,
			// A wildcard segment, not a literal: this is a whole-map invariant, and
			// "multitenant.tenants.messaging" would be indistinguishable from a tenant
			// actually named "messaging" (tenantField's sentinel emits exactly that).
			Field:   "multitenant.tenants.*.messaging",
			Message: "inconsistent configuration",
			Action:  "either all tenants must have messaging configured or none should",
		}
	}
	return nil
}

// checkTenantMessagingReachable rejects per-tenant messaging blocks that shared
// tenancy would never read: under messaging.tenancy: shared every consumer and
// publisher resolves the control-plane key, so a tenant broker URL is a silently
// dead setting rather than a working per-tenant broker.
func checkTenantMessagingReachable(tenants map[string]TenantEntry, msg *MessagingConfig) error {
	if msg.Tenancy != TenancyShared {
		return nil
	}
	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if isTenantMessagingConfigured(&tenant.Messaging) {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    "multitenant.tenants.*.messaging",
				Message:  "unreachable under messaging.tenancy: " + TenancyShared,
				Action:   "remove the per-tenant messaging blocks or set messaging.tenancy: " + TenancyPerTenant,
			}
		}
	}
	return nil
}

// normalizeTenantCache shapes a tenant's cache configuration with the same
// fail-fast posture as the tenant database: an enabled-but-misconfigured
// cache must crash at startup, not at the first per-request cache access (see
// tenant_store.go CacheConfig). Per-tenant cache keys have no koanf defaults,
// so the type defaults to redis here before normalizeCache fills the rest.
func normalizeTenantCache(cache *CacheConfig) error {
	if cache.Enabled && cache.Type == "" {
		cache.Type = CacheTypeRedis
	}
	// per-tenant caches only exist in multi-tenant mode
	return normalizeCache(cache, true)
}

// checkTenantCache is checkCache addressed to the tenant. The tenant travels in Field, not
// in a wrapping message: a consumer matching on ConfigError.Field could not otherwise tell
// which tenant's cache failed, and the database sections next door already spell it this
// way (C60.19). The addressing itself is the exported door, so the startup and runtime cache
// doors cannot drift apart.
func checkTenantCache(tenantID string, cache *CacheConfig) error {
	return QualifyCacheConfigErrorForKey(checkCache(cache), tenantID)
}

// validateSourceConfig validates the source configuration type
func validateSourceConfig(cfg *SourceConfig) error {
	if cfg.Type != SourceTypeStatic && cfg.Type != SourceTypeDynamic {
		return NewInvalidFieldError("source.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), []string{"static", "dynamic"})
	}
	return nil
}
