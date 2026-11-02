package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gaborage/go-bricks/server"
)

const tenantFamilyGlobalMiddlewareHint = "global middleware runs before a family resolves its tenant, so it would see no tenant on the family's routes — " +
	"attach each gate per group instead: in the family's Authenticate (before the tenant) or Use (after it), and on every other group that needs it"

// tenantFamilyLister is the server door listing each tenant-owned route family's full prefix.
type tenantFamilyLister interface {
	TenantFamilyPrefixes() []string
}

// attributedFamily is one tenant-owned family and the module whose routes built it ("" for none).
type attributedFamily struct {
	prefix, module string
}

// checkTenantFamilyGlobalMiddleware fails startup when global middleware is installed beside a
// tenant-owned route family (ADR-164). Beside a family it first WARNs once per GlobalMiddleware
// implementer that installs none. A server missing either door is skipped; *server.Server has
// both, and its Start refuses the same pair.
func (a *App) checkTenantFamilyGlobalMiddleware() error {
	lister, ok := a.server.(tenantFamilyLister)
	if !ok {
		return nil
	}
	counter, ok := a.server.(interface{ GlobalMiddlewareCount() int })
	if !ok {
		return nil
	}
	prefixes := lister.TenantFamilyPrefixes()
	if len(prefixes) == 0 {
		return nil
	}
	var modules []string
	fromModules := 0
	for _, src := range a.registry.globalMiddlewareSources {
		if src.installed == 0 {
			a.logger.Warn().Str("module", src.module).
				Msgf("module %q implements GlobalMiddleware but installs none beside a tenant-owned route family; startup refuses this binary wherever it installs one (ADR-164)", src.module)
			continue
		}
		modules = append(modules, src.module)
		fromModules += src.installed
	}
	count := counter.GlobalMiddlewareCount()
	if count == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s; %s; %s", server.ErrTenantFamilyWithGlobalMiddleware,
		familyClause(attributeFamilies(prefixes, a.registry.routeSpans)), middlewareClause(modules, count-fromModules),
		tenantFamilyGlobalMiddlewareHint)
}

// attributeFamilies pairs each family with its module: family i belongs to span k when
// spans[k].families <= i < spans[k+1].families, and to no module otherwise.
func attributeFamilies(prefixes []string, spans []routeSpan) []attributedFamily {
	out := make([]attributedFamily, len(prefixes))
	for i, prefix := range prefixes {
		out[i].prefix = prefix
	}
	for k := 0; k+1 < len(spans); k++ {
		for i := spans[k].families; i < spans[k+1].families; i++ {
			out[i].module = spans[k].module
		}
	}
	return out
}

func familyClause(families []attributedFamily) string {
	parts := make([]string, len(families))
	for i, f := range families {
		owner := "no module"
		if f.module != "" {
			owner = "module " + strconv.Quote(f.module)
		}
		parts[i] = f.prefix + " (" + owner + ")"
	}
	label := "family "
	if len(families) > 1 {
		label = "families "
	}
	return label + strings.Join(parts, ", ")
}

func middlewareClause(modules []string, direct int) string {
	if len(modules) == 0 {
		return fmt.Sprintf("global middleware: %d registered directly on the server", direct)
	}
	quoted := make([]string, len(modules))
	for i, m := range modules {
		quoted[i] = strconv.Quote(m)
	}
	clause := "global middleware from module(s) " + strings.Join(quoted, ", ")
	if direct > 0 {
		clause += fmt.Sprintf(" and %d registered directly on the server", direct)
	}
	return clause
}
