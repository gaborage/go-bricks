package config

// QualifyCacheConfigErrorForKey addresses a cache configuration error to the resource key that
// produced it. It is a separate function rather than a parameter on the checks for the reason
// ApplyDatabasePoolDefaultsForKey is: the key is known to the per-key doors and to nobody else,
// so a caller that has no key cannot be made to pass one.
//
// The empty key is the ROOT cache section and returns err untouched — the same error value, not
// a copy — which is what keeps a single-tenant deployment's errors byte-identical. Any other key
// is a tenant id: Field becomes multitenant.tenants.<key>.cache.<leaf> and a generated hint is
// re-pointed to match, dropping its env half when the qualified key does not round-trip through
// an environment variable. There is no named-cache spelling — unlike databases, caches have no
// named siblings, so a non-empty key is always a tenant.
func QualifyCacheConfigErrorForKey(err error, resourceKey string) error {
	if err == nil || resourceKey == "" {
		return err
	}
	return qualifyConfigError(err, "multitenant.tenants."+resourceKey+".cache", func(field string) string {
		return tenantField(resourceKey, cacheLeaf(field))
	})
}

// cacheLeaf reads one root-spelled cache field as the leaf it names under a cache section, the
// way dbSection.qualifyField reads a database one: the bare section name and the empty field
// both name the section itself, a cache.<leaf> keeps its leaf, and anything else — a field no
// cache check emits today — is kept under the section rather than dropped.
func cacheLeaf(field string) string {
	if field == "" {
		return fieldCache
	}
	if leaf, ok := reattachHead(field, fieldCache, fieldCache); ok {
		return leaf
	}
	return fieldCache + "." + field
}
