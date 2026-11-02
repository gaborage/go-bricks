package auth

import (
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/server"
)

// errNoPrincipal is TenantClaim's wiring fault. It is a plain error, so a family's
// fault rule answers it as a server fault (500) and never hands it to Deny.
var errNoPrincipal = errors.New("auth: TenantClaim found no Principal in the request context; put auth.Middleware in the family's Authenticate steps")

// TenantClaim reads the named claim from the Principal auth.Middleware attached in Authenticate.
//
// It is a tenant-owned family's TenantPolicy.Tenant: put auth.Middleware(v) in the
// family's Authenticate steps. The claim must be a JSON string inside the tenant-ID
// grammar (multitenant.ValidateTenantID, ADR-162). It is never trimmed or mapped, so
// an issuer identity outside the grammar needs its own TenantFunc. It returns:
//   - a plain error when the request carries no Principal: a wiring fault, answered 500;
//   - an error wrapping multitenant.ErrTenantResolutionFailed for an absent, empty or
//     non-string claim: a client fault, answered by the family's Deny;
//   - an error wrapping multitenant.ErrInvalidTenantID for a claim outside the
//     grammar: a client fault.
//
// No error carries the claim's value. TenantClaim panics on an empty claim name.
func TenantClaim(claim string) server.TenantFunc {
	if claim == "" {
		panic("auth.TenantClaim: empty claim name")
	}
	return func(c server.HandlerContext) (string, error) {
		p, ok := PrincipalFromContext(c.RequestContext())
		if !ok {
			return "", errNoPrincipal
		}
		v, ok := p.Claim(claim)
		if !ok {
			return "", fmt.Errorf("auth: tenant claim %q is absent: %w", claim, multitenant.ErrTenantResolutionFailed)
		}
		tenantID, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("auth: tenant claim %q is a %T, not a string: %w", claim, v, multitenant.ErrTenantResolutionFailed)
		}
		if tenantID == "" {
			return "", fmt.Errorf("auth: tenant claim %q is empty: %w", claim, multitenant.ErrTenantResolutionFailed)
		}
		if err := multitenant.ValidateTenantID(tenantID); err != nil {
			return "", fmt.Errorf("auth: tenant claim %q: %w", claim, err)
		}
		return tenantID, nil
	}
}
