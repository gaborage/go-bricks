package commands

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
)

// tlsValidatingProvider wraps a DBConfigProvider so every resolved config passes the
// same vendor validation go-bricks applies before it dials (ADR-062 and the rules
// around it). Wrapping the provider rather than the file loader is what covers the AWS
// Secrets Manager source too, which resolves lazily per tenant and never touches the
// YAML path.
//
// The CLI is a separate module pinning a released go-bricks, so this is deliberately
// the exported seam rather than a local copy of the rules: config.ApplyDatabasePoolDefaults
// runs the vendor validators plus type inference from a recognized connectionstring
// scheme. It is wider than database.tls alone — it also enforces Oracle's connection
// identifier rules and time.LoadLocation on database.timezone — which is the intended
// posture: a config the service will refuse to boot on should not pass the migrate CLI.
type tlsValidatingProvider struct {
	inner database.DBConfigProvider
}

// DBConfig resolves the inner provider's config and fails closed on one the framework
// would reject. The tenant key is named in the error because a fleet run resolves one
// config per tenant, and "which tenant" is the operator's first question.
//
// Validation runs on a COPY. config.TenantStore hands back a cached, shared pointer for
// the single-tenant key ("" -> its defaultDB) and for "named:" keys, and the seam
// normalizes in place, so validating the original would mutate configuration other
// callers still hold — and, under --parallel, would be an unsynchronized write from
// several goroutines. DatabaseConfig is flat scalars, so the copy is fixed-size and
// cheap; it removes the whole aliasing class instead of relying on every present and
// future DBConfigProvider to return a freshly allocated config.
func (p *tlsValidatingProvider) DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error) {
	cfg, err := p.inner.DBConfig(ctx, key)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return cfg, nil
	}
	validated := *cfg
	if err := config.ApplyDatabasePoolDefaults(&validated); err != nil {
		if key == "" {
			return nil, err
		}
		return nil, fmt.Errorf("tenant %q: %w", key, err)
	}
	return &validated, nil
}
