package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
}

// ErrInvalidPrefix indicates the configured prefix is unusable.
var ErrInvalidPrefix = errors.New("migration: invalid secrets prefix (must end with '/')")

// ErrSecretMalformed indicates the secret payload could not be parsed into a
// usable DatabaseConfig in either canonical or RDS-rotation form.
var ErrSecretMalformed = errors.New("migration: secret payload malformed")

// ErrNoFetcher indicates the SecretsProvider was constructed without a Fetch function.
var ErrNoFetcher = errors.New("migration: SecretsProvider.Fetch is nil")

// Validate checks that the provider is wired correctly. Call eagerly at startup
// so misconfiguration surfaces before any tenant lookup.
func (p *SecretsProvider) Validate() error {
	if p.Fetch == nil {
		return ErrNoFetcher
	}
	if p.Prefix != "" && !strings.HasSuffix(p.Prefix, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidPrefix, p.Prefix)
	}
	return nil
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
	if p.Fetch == nil {
		return nil, ErrNoFetcher
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

// secretPayload mirrors both the canonical go-bricks DatabaseConfig shape and
// the AWS-managed RDS rotation shape. Canonical fields take precedence when
// both are present.
type secretPayload struct {
	// Canonical (go-bricks DatabaseConfig field names)
	Type             string `json:"type"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Database         string `json:"database"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	ConnectionString string `json:"connectionstring"`

	// RDS rotation fallback aliases
	Engine string `json:"engine"`
	DBName string `json:"dbname"`
}

// parseSecretPayload decodes the secret bytes into a *config.DatabaseConfig.
// The canonical shape is tried first; if Type/Database are empty, the parser
// falls back to the AWS RDS rotation shape (engine/dbname). Returns
// ErrSecretMalformed if neither shape produces a usable config.
func parseSecretPayload(raw []byte) (*config.DatabaseConfig, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrSecretMalformed)
	}

	var p secretPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSecretMalformed, err)
	}

	dbType := p.Type
	if dbType == "" {
		dbType = normalizeEngine(p.Engine)
	}

	dbName := p.Database
	if dbName == "" {
		dbName = p.DBName
	}

	if dbType == "" || p.Host == "" || p.Username == "" {
		return nil, fmt.Errorf("%w: missing required fields (type/engine, host, username)", ErrSecretMalformed)
	}

	return &config.DatabaseConfig{
		Type:             dbType,
		Host:             p.Host,
		Port:             p.Port,
		Database:         dbName,
		Username:         p.Username,
		Password:         p.Password,
		ConnectionString: p.ConnectionString,
	}, nil
}

// normalizeEngine maps AWS-managed engine names to go-bricks vendor strings.
func normalizeEngine(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "postgresql", "aurora-postgresql":
		return config.PostgreSQL
	case "oracle", "oracle-ee", "oracle-se2":
		return config.Oracle
	case "":
		return ""
	default:
		return engine
	}
}
