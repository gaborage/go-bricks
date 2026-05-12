package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gaborage/go-bricks/config"
)

// DefaultSecretsPrefix is the default name prefix used to look up tenant
// database credentials in a secret store. The full secret name is
// DefaultSecretsPrefix + tenantID.
const DefaultSecretsPrefix = "gobricks/migrate/"

// SecretFetcher resolves an opaque secret name to its raw payload bytes.
// The framework stays decoupled from any specific cloud SDK; callers wire
// AWS Secrets Manager, HashiCorp Vault, or another store behind this seam.
type SecretFetcher func(ctx context.Context, secretName string) ([]byte, error)

// SecretsProvider implements database.DBConfigProvider on top of a SecretFetcher.
// It composes the secret name as Prefix + tenantID, fetches the bytes, and
// parses them as either the canonical go-bricks DatabaseConfig shape or the
// AWS-managed RDS rotation shape.
type SecretsProvider struct {
	// Prefix is prepended to each tenant ID when composing the secret name.
	// Empty defaults to DefaultSecretsPrefix at lookup time.
	Prefix string

	// Fetch resolves a secret name to its payload. Required.
	Fetch SecretFetcher

	validateOnce sync.Once
	validateErr  error
}

// ErrInvalidPrefix indicates the configured prefix is unusable.
var ErrInvalidPrefix = errors.New("migration: invalid secrets prefix (must end with '/')")

// ErrSecretMalformed indicates the secret payload could not be parsed into a
// usable DatabaseConfig in either canonical or RDS-rotation form.
var ErrSecretMalformed = errors.New("migration: secret payload malformed")

// ErrNoFetcher indicates the SecretsProvider was constructed without a Fetch function.
var ErrNoFetcher = errors.New("migration: SecretsProvider.Fetch is nil")

// ErrEmptyTenantID is returned when DBConfig is invoked with a blank tenant ID.
var ErrEmptyTenantID = errors.New("migration: tenantID is empty")

// Validate checks that the provider is wired correctly. Callers may invoke it
// eagerly at startup; DBConfig also calls it lazily on first lookup so library
// callers who skip the explicit check still get a clear error before any
// tenant fetch.
func (p *SecretsProvider) Validate() error {
	p.validateOnce.Do(func() {
		if p.Fetch == nil {
			p.validateErr = ErrNoFetcher
			return
		}
		if p.Prefix != "" && !strings.HasSuffix(p.Prefix, "/") {
			p.validateErr = fmt.Errorf("%w: %q", ErrInvalidPrefix, p.Prefix)
		}
	})
	return p.validateErr
}

// SecretName composes the full secret name for the given tenant ID using the
// provider's prefix (or DefaultSecretsPrefix when unset).
func (p *SecretsProvider) SecretName(tenantID string) string {
	prefix := p.Prefix
	if prefix == "" {
		prefix = DefaultSecretsPrefix
	}
	return prefix + tenantID
}

// DBConfig satisfies database.DBConfigProvider. Looks up the tenant's secret,
// parses the payload, and returns the resulting DatabaseConfig.
func (p *SecretsProvider) DBConfig(ctx context.Context, tenantID string) (*config.DatabaseConfig, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}

	name := p.SecretName(tenantID)
	raw, err := p.Fetch(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("fetch secret %q for tenant %q: %w", name, tenantID, err)
	}

	cfg, err := parseSecretPayload(raw)
	if err != nil {
		return nil, fmt.Errorf("parse secret %q for tenant %q: %w", name, tenantID, err)
	}
	return cfg, nil
}

// rdsRotationAliases carries the AWS-managed RDS rotation fields that don't
// share JSON tags with config.DatabaseConfig. The canonical shape is decoded
// directly into *config.DatabaseConfig; only these aliases need a parallel
// struct.
type rdsRotationAliases struct {
	Engine string `json:"engine"`
	DBName string `json:"dbname"`
}

// parseSecretPayload decodes the secret bytes into a *config.DatabaseConfig.
// The canonical go-bricks shape is decoded directly; any blank Type/Database
// is then filled from the AWS RDS rotation aliases (engine/dbname). Returns
// ErrSecretMalformed if neither shape produces a usable config.
func parseSecretPayload(raw []byte) (*config.DatabaseConfig, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrSecretMalformed)
	}

	var cfg config.DatabaseConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSecretMalformed, err)
	}

	var aliases rdsRotationAliases
	if err := json.Unmarshal(raw, &aliases); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSecretMalformed, err)
	}

	if cfg.Type == "" {
		cfg.Type = normalizeEngine(aliases.Engine)
	}
	if cfg.Database == "" {
		cfg.Database = aliases.DBName
	}

	if cfg.Type == "" || cfg.Host == "" || cfg.Username == "" {
		return nil, fmt.Errorf("%w: missing required fields (type/engine, host, username)", ErrSecretMalformed)
	}

	return &cfg, nil
}

// normalizeEngine maps AWS-managed engine names to go-bricks vendor strings.
func normalizeEngine(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", config.PostgreSQL, "aurora-postgresql":
		return config.PostgreSQL
	case config.Oracle, "oracle-ee", "oracle-se2":
		return config.Oracle
	case "":
		return ""
	default:
		return engine
	}
}
