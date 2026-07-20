package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/go-viper/mapstructure/v2"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/configdecode"
)

// tenantIDPattern enforces a conservative character allowlist on tenant IDs
// before they are concatenated into secret-store lookup names. AWS Secrets
// Manager has no path-traversal semantics, so this is defense-in-depth: a
// compromised control-plane returning malicious tenant IDs cannot craft
// unexpected secret names (whitespace, control chars, slashes, dots).
var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

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

	// NameFor, when non-nil, composes the secret name for a tenant instead
	// of the default Prefix + tenantID. The tenantID it receives has already
	// been trimmed and allowlist-validated. Use it for grammars that place
	// segments after the tenant ID (e.g. "/env/platform/<id>/db"). The
	// returned name is used as-is apart from a blank-name check — the
	// composer owns its correctness. When NameFor is set, Prefix is ignored
	// for lookups but a non-empty Prefix is still checked by Validate (a
	// malformed Prefix alongside NameFor fails fast rather than lingering).
	NameFor func(tenantID string) (string, error)

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

// ErrInvalidTenantID is returned when DBConfig is invoked with a tenant ID
// that contains characters outside the [A-Za-z0-9_-] allowlist or exceeds
// the 128-character length bound.
var ErrInvalidTenantID = errors.New("migration: tenantID contains characters outside [A-Za-z0-9_-] or exceeds 128 characters")

// ErrNameForFailed indicates the custom NameFor hook returned an error or
// a blank (empty/whitespace-only) name for a validated tenant ID.
var ErrNameForFailed = errors.New("migration: NameFor failed to compose a secret name")

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
// provider's prefix (or DefaultSecretsPrefix when unset). When NameFor is
// set, DBConfig uses it instead and this method reflects only the default
// Prefix + tenantID composition.
func (p *SecretsProvider) SecretName(tenantID string) string {
	prefix := p.Prefix
	if prefix == "" {
		prefix = DefaultSecretsPrefix
	}
	return prefix + tenantID
}

// secretNameFor resolves the effective secret name: NameFor when set,
// otherwise the default Prefix + tenantID composition. tenantID must already
// be validated by the caller.
func (p *SecretsProvider) secretNameFor(tenantID string) (string, error) {
	if p.NameFor == nil {
		return p.SecretName(tenantID), nil
	}
	name, err := p.NameFor(tenantID)
	if err != nil {
		return "", fmt.Errorf("%w for tenant %q: %w", ErrNameForFailed, tenantID, err)
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%w: blank name for tenant %q", ErrNameForFailed, tenantID)
	}
	return name, nil
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
	if !tenantIDPattern.MatchString(tenantID) {
		return nil, ErrInvalidTenantID
	}

	name, err := p.secretNameFor(tenantID)
	if err != nil {
		return nil, err
	}
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

	// Decode to a generic map first so numeric duration fields (pool.idle.time,
	// lifetime.max, keepalive.interval, query.slow.threshold, manager.*) route through the
	// guard hook instead of JSON-coercing a bare number straight to nanoseconds.
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSecretMalformed, err)
	}

	var cfg config.DatabaseConfig
	if err := decodeSecretConfig(payload, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSecretMalformed, err)
	}

	var aliases rdsRotationAliases
	if err := json.Unmarshal(raw, &aliases); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSecretMalformed, err)
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

// decodeSecretConfig maps a JSON-decoded payload into a DatabaseConfig via mapstructure,
// routing numeric time.Duration fields through the shared guard so a bare JSON number
// (e.g. keepalive.interval: 60) is rejected, not silently coerced to nanoseconds. TagName
// "json" matches the struct's json tags; WeaklyTypedInput handles float64 (JSON's numeric
// type) -> int and preserves the prior encoding/json numeric-coercion behavior.
func decodeSecretConfig(payload map[string]any, cfg *config.DatabaseConfig) error {
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			configdecode.NumericToDurationGuardHookFunc(),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
		TagName:          "json",
		Result:           cfg,
	})
	if err != nil {
		return err
	}
	return dec.Decode(payload)
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
