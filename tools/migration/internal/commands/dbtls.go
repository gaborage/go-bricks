package commands

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
)

// The block below mirrors the database.tls validation go-bricks applies to
// framework-loaded configs (ADR-062, github.com/gaborage/go-bricks
// config/validation.go: validatePostgreSQLFields, validateOracleFields,
// inferDatabaseTypeFromConnectionString, pgSSLModes, pgTLSMandatorySSLModes).
// The CLI is a separate Go module whose standalone `go install` pins a RELEASED
// go-bricks, so it cannot reach the framework's unexported validators. Nor can it
// borrow the exported config.ApplyDatabasePoolDefaults seam: at the pinned v0.58.1
// that function is `return applyDatabasePoolDefaults(cfg)` and performs NO vendor
// validation at all — the validateVendorSpecificFields call it carries on main was
// added afterwards, by #1002. Keep the two in sync: rule set, check order, and
// message text. Two deviations are unavoidable and deliberate — the framework's
// unexported errCategoryInvalid and fieldDatabaseTLS constants appear here as their
// literal values.
//
// Deletion trigger: once the CLI's pin carries these rules, this file is replaced by
// a call to config.ApplyDatabasePoolDefaults. That swap also picks up Oracle's
// connection-identifier rules and time.LoadLocation on database.timezone, which are
// the real deltas — its pool/timezone defaulting is inert here, since no non-test
// file under tools/migration reads Pool, Timezone, or Query.
//
// Scope is TLS only.
const (
	sslModeDisable    = "disable"
	sslModeAllow      = "allow"
	sslModePrefer     = "prefer"
	sslModeRequire    = "require"
	sslModeVerifyCA   = "verify-ca"
	sslModeVerifyFull = "verify-full"

	fieldDatabaseTLS = "database.tls"
	categoryInvalid  = "invalid"
)

// pgSSLModes mirrors the sslmode values pgx v5 accepts in configTLS; anything
// else fails only at connect time with a redacted parse error, so gate it here.
var pgSSLModes = []string{sslModeDisable, sslModeAllow, sslModePrefer, sslModeRequire, sslModeVerifyCA, sslModeVerifyFull}

// pgTLSMandatorySSLModes are the modes under which pgx is guaranteed to use
// configured TLS material; the opportunistic modes silently discard or
// downgrade it.
var pgTLSMandatorySSLModes = []string{sslModeRequire, sslModeVerifyCA, sslModeVerifyFull}

// inferDatabaseTypeFromConnectionString mirrors the framework helper of the same
// name (ADR-050). Without it a DSN-only config would carry an empty Type, and the
// vendor dispatch below would validate nothing — the silent gap this whole file exists
// to close.
func inferDatabaseTypeFromConnectionString(cs string) string {
	lower := strings.ToLower(strings.TrimSpace(cs))
	switch {
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"):
		return config.PostgreSQL
	case strings.HasPrefix(lower, "oracle://"):
		return config.Oracle
	}
	return ""
}

// validateDatabaseTLS rejects the database.tls shapes the driver would silently
// discard or downgrade, matching what go-bricks enforces at startup. It trims the
// TLS fields in place first, so the DSN builders downstream (controlPlaneDSN, the
// Flyway URL) see the same canonical values the validation judged — a config read
// from a YAML file or an AWS secret carries whitespace until proven otherwise.
//
// Vendor is taken from Type, falling back to the connectionstring scheme; an
// unrecognized pair leaves the config unvalidated, exactly as the framework's
// default switch arm does.
func validateDatabaseTLS(cfg *config.DatabaseConfig) error {
	if cfg == nil {
		return nil
	}

	cfg.TLS.Mode = strings.TrimSpace(cfg.TLS.Mode)
	cfg.TLS.CertFile = strings.TrimSpace(cfg.TLS.CertFile)
	cfg.TLS.KeyFile = strings.TrimSpace(cfg.TLS.KeyFile)
	cfg.TLS.CAFile = strings.TrimSpace(cfg.TLS.CAFile)

	dbType := cfg.Type
	if dbType == "" {
		dbType = inferDatabaseTypeFromConnectionString(cfg.ConnectionString)
	}

	switch dbType {
	case config.Oracle:
		return validateOracleTLS(cfg)
	case config.PostgreSQL:
		return validatePostgreSQLTLS(cfg)
	default:
		return nil
	}
}

// validatePostgreSQLTLS mirrors validatePostgreSQLFields' TLS rules. Check order is
// load-bearing: connectionstring short-circuits, then the mode allowlist, then the
// material/mode coherence rule, then the cert/key pairing.
func validatePostgreSQLTLS(cfg *config.DatabaseConfig) error {
	if cfg.ConnectionString != "" {
		if cfg.TLS.Mode != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != "" {
			return &config.ConfigError{
				Category: categoryInvalid,
				Field:    fieldDatabaseTLS,
				Message:  "database.tls is ignored when connectionstring is set (the DSN is used verbatim)",
				Action:   "move TLS settings into the connection string (sslmode/sslrootcert/sslcert/sslkey) and remove the database.tls block",
			}
		}
		return nil
	}

	if cfg.TLS.Mode != "" && !slices.Contains(pgSSLModes, cfg.TLS.Mode) {
		return config.NewInvalidFieldError("database.tls.mode", fmt.Sprintf("invalid value: %s", cfg.TLS.Mode), pgSSLModes)
	}

	hasMaterial := cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != ""
	if hasMaterial && !slices.Contains(pgTLSMandatorySSLModes, cfg.TLS.Mode) {
		return &config.ConfigError{
			Category: categoryInvalid,
			Field:    fieldDatabaseTLS,
			Message: "TLS cert/key/ca require a mode that guarantees TLS; under disable/allow/prefer " +
				"(or an unset mode, which defaults to prefer) pgx silently discards the material, downgrades " +
				"to plaintext, or (for ca: system) silently upgrades the mode — none of which is what the config says",
			Action: "set database.tls.mode to require, verify-ca, or verify-full",
		}
	}

	// Client-certificate (mTLS) auth requires BOTH sslcert and sslkey; pgx rejects a lone
	// one only at connect time, where the parse error is redacted.
	if (cfg.TLS.CertFile != "") != (cfg.TLS.KeyFile != "") {
		return &config.ConfigError{
			Category: categoryInvalid,
			Field:    fieldDatabaseTLS,
			Message:  "sslcert and sslkey must be configured together for client-certificate (mTLS) auth",
			Action:   "set both database.tls.cert and database.tls.key, or neither",
		}
	}
	return nil
}

// validateOracleTLS mirrors validateOracleFields' TLS rule: Oracle TLS (tcps/wallet)
// is not implemented in go-bricks, so reject the whole database.tls block rather than
// silently ignoring it — mode included, since nothing wires the TLS it implies, leaving
// an operator to believe the connection is encrypted.
func validateOracleTLS(cfg *config.DatabaseConfig) error {
	if cfg.TLS.Mode != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != "" {
		return &config.ConfigError{
			Category: categoryInvalid,
			Field:    fieldDatabaseTLS,
			Message:  "database.tls settings are not supported for Oracle (tcps/wallet is not implemented)",
			Action:   "remove the database.tls block for Oracle connections",
		}
	}
	return nil
}

// tlsValidatingProvider wraps a DBConfigProvider so every resolved config passes
// validateDatabaseTLS before any caller builds a DSN from it. Wrapping the provider
// rather than the file loader is what covers the AWS Secrets Manager source too, which
// resolves lazily per tenant and never touches the YAML path.
type tlsValidatingProvider struct {
	inner database.DBConfigProvider
}

// DBConfig resolves the inner provider's config and fails closed on a rejected
// database.tls block. The tenant key is named in the error because a fleet run
// resolves one config per tenant, and "which tenant" is the operator's first question.
//
// Validation runs on a COPY. config.TenantStore hands back a cached, shared pointer for
// the single-tenant key ("" -> its defaultDB) and for "named:" keys, so trimming the
// original in place would mutate configuration other callers still hold — and, under
// --parallel, would be an unsynchronized write to the same string fields from several
// goroutines. DatabaseConfig is flat scalars, so the copy is fixed-size and cheap; it
// removes the whole aliasing class instead of relying on every present and future
// DBConfigProvider to return a freshly allocated config.
func (p *tlsValidatingProvider) DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error) {
	cfg, err := p.inner.DBConfig(ctx, key)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return cfg, nil
	}
	validated := *cfg
	if err := validateDatabaseTLS(&validated); err != nil {
		if key == "" {
			return nil, err
		}
		return nil, fmt.Errorf("tenant %q: %w", key, err)
	}
	return &validated, nil
}
