# ADR-003: Database by Intent Configuration

**Date:** 2025-09-17
**Status:** Accepted
**Context:** Deterministic database configuration and database-free application support

## Problem Statement

The framework previously included database defaults in configuration loading, creating ambiguity about whether database functionality was intentionally enabled. This prevented clean database-free applications and made configuration behavior non-deterministic.

Applications requiring no database access (pure APIs, microservices) were forced to provide dummy database configuration or work around framework assumptions about database availability.

## Decision

Implement "database by intent" configuration with the following principles:

1. **No Database Defaults**: Remove all database-related defaults from `config.go`
2. **Explicit Configuration Required**: Database is only enabled when explicitly configured
3. **Deterministic Behavior**: Same configuration always produces same result
4. **Conditional Validation**: Skip database validation when not configured

## Implementation

### Configuration Changes
- **Removed**: All database defaults from `loadDefaults()` in `config/config.go`
- **Added**: `IsDatabaseConfigured()` function in `config/validation.go`
- **Logic**: Database enabled when `ConnectionString != ""` OR `Host != ""` OR `Type != ""`

### Validation Changes
- **Before**: Database validation always required, caused failures for database-free apps
- **After**: `validateDatabase()` returns early when `!IsDatabaseConfigured()`
- **Consistency**: Shared logic between validation and runtime via `IsDatabaseConfigured()`

### Runtime Integration
- **Updated**: `app.isDatabaseEnabled()` to use shared `config.IsDatabaseConfigured()`
- **Behavior**: Module dependency injection skips database when not configured

## Consequences

### Positive
- **Deterministic**: Configuration behavior is predictable and explicit
- **Database-Free Support**: Applications can run without any database configuration
- **Clear Intent**: Explicit configuration signals intentional database usage
- **Reduced Confusion**: No ambiguity about database enablement

### Negative
- **Breaking Change**: Applications relying on database defaults must update configuration
- **More Explicit**: Requires intentional database configuration in all database-using applications

## Migration Impact

Existing applications using database functionality must explicitly configure:
- Connection string, OR
- Host + type combination

No migration needed for database-free applications.
