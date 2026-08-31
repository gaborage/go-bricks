package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// placement is where a resource kind's section sits in the configuration tree. It decides
// whether the section may be absent, whether a manager block is allowed (database only), and
// how its errors are addressed. Not every kind uses every value: a cache section is never
// placementNamed — unlike databases, caches have no named siblings.
type placement int

const (
	placementRoot   placement = iota // the kind's own top-level block — may be absent (ADR-047)
	placementNamed                   // databases.<name> (database only)
	placementTenant                  // multitenant.tenants.<id>.<kind>
)

// resourceKind resolves a resource key to the section it names, for one config section family
// the addressing engine knows how to place. The key vocabulary belongs to each kind's own
// manager and is unchanged by this engine: "" is always the root, a NamedDatabasePrefix key is
// a named database (database only), and anything else is a tenant id.
//
// kindDatabase and kindCache are the two resource kinds this engine addresses today (#1260
// scope: database + cache). A messaging kind joins the day a messaging door actually needs
// addressing — today that would be a one-adapter hypothetical seam.
type resourceKind func(key string) section

var (
	kindDatabase resourceKind = sectionForResourceKey
	kindCache    resourceKind = cacheSectionForResourceKey
)

// section names one resource-kind section: the root-spelled field its kind's error constructors
// emit, its path in the tree, and its placement. This is the addressing engine's one
// representation for every section it qualifies — the database sections (root/named/tenant) and
// the cache sections (root/tenant) alike. Concentrating both here is what replaced
// cache_section.go's bespoke string-splicing: the same reattach-and-prefix rule now runs once,
// for every kind, instead of once per kind.
type section struct {
	rootField string
	path      string
	placement placement
}

// rootCacheSection is the root cache block: `cache`. It may be absent or disabled — that is
// checkCache's call, not the addressing engine's — and its errors are returned untouched.
func rootCacheSection() section {
	return section{rootField: fieldCache, path: fieldCache, placement: placementRoot}
}

// tenantCacheSection is one tenant's cache block: `multitenant.tenants.<id>.cache`. Caches have
// no named siblings — unlike databases, a non-empty cache resource key is always a tenant id.
func tenantCacheSection(id string) section {
	return section{rootField: fieldCache, path: "multitenant.tenants." + id + ".cache", placement: placementTenant}
}

// cacheSectionForResourceKey maps a cache resource key onto the section it resolves, the cache
// half of the vocabulary sectionForResourceKey (database_section.go) translates: "" is the root
// cache, anything else is a tenant id.
func cacheSectionForResourceKey(key string) section {
	if key == "" {
		return rootCacheSection()
	}
	return tenantCacheSection(key)
}

// QualifyCacheConfigErrorForKey addresses a cache configuration error to the resource key that
// produced it. It is the cache door onto the shared addressing engine, the way
// ApplyDatabasePoolDefaultsForKey is the database door — both resolve a resource key to a
// section via their kind and let section.qualify do the rewriting, so the two kinds cannot
// drift apart the way cache_section.go's own copy of this recipe once did (ADR-076).
//
// A nil err is returned nil regardless of key: the engine's qualify is never invoked with one
// (every other caller only qualifies an err it already knows is non-nil), so this guard is the
// door's own convenience rather than a property of the shared engine.
func QualifyCacheConfigErrorForKey(err error, resourceKey string) error {
	if err == nil {
		return nil
	}
	return kindCache(resourceKey).qualify(err)
}

// qualify re-addresses an error raised against the section's kind's root spelling to this
// section, so a consumer matching on ConfigError.Field learns WHICH section failed. The root
// placement returns the error untouched — the SAME value, not a copy — which is what keeps a
// single-tenant deployment's errors byte-identical; every other placement gets a rewritten copy.
//
// The path is carried by Field alone, never also by a wrapping message: printing it in both
// places is how the same section path ends up in one error twice.
func (s section) qualify(err error) error {
	if s.placement == placementRoot {
		return err
	}
	return qualifyConfigError(err, s.path, s.qualifyField)
}

// qualifyField rewrites one root-spelled field to this section. A key under the kind's own root
// field swaps that head for the section path, so "database.host" reads "databases.reporting.host"
// and "cache.redis.host" reads "multitenant.tenants.acme.cache.redis.host" — the tenant spelling
// keeps its own trailing kind segment. A field that is not key-shaped — the Oracle
// connection-identifier check names one — is prefixed instead, which keeps the offending name
// rather than dropping it.
func (s section) qualifyField(field string) string {
	if field == "" {
		return s.path
	}
	if qualified, ok := reattachHead(field, s.rootField, s.path); ok {
		return qualified
	}
	return s.path + "." + field
}

// qualifyConfigError re-addresses a ConfigError to a subtree: Field through addressField,
// Action re-pointed to match, Details cloned so the copy owns them. A non-ConfigError has no
// field to move, so it is wrapped with path instead — the only place a path is allowed into
// the message rather than the field.
//
// The one addressing primitive every kind's non-root section.qualify shares, so a producer
// that reimplements this recipe instead of calling it is exactly the drift #1260 closes.
func qualifyConfigError(err error, path string, addressField func(string) string) error {
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		return fmt.Errorf("%s: %w", path, err)
	}
	qualified := *cfgErr
	qualified.Field = addressField(cfgErr.Field)
	qualified.Action = requalifyAction(cfgErr.Action, cfgErr.Field, qualified.Field)
	qualified.Details = slices.Clone(cfgErr.Details)
	return &qualified
}

// requalifyAction re-points a generated "set X env var or add Y to config.yaml" hint at the
// qualified key. It rewrites only a hint this package generated, recognized by rebuilding that
// hint for the key the hint itself names and comparing — so a hand-written Action, and one
// naming a key outside the field being qualified, are both left exactly as they are.
//
// The key in the hint is not always the Field: NewNotConfiguredError puts the FEATURE in Field
// ("cache") and the YAML path in the hint ("cache.enabled"), behind a "to enable: " lead-in. So
// the key is read out of the hint and re-pointed by the same field-to-field move, which is what
// keeps that hint from surviving qualification and sending an operator at the root key.
//
// A hint shape this function does not recognize is left as it is, which is the safe direction but
// not a free one: a future constructor whose Action does not rebuild from missingFieldAction —
// a different lead-in, or two keys in one hint — keeps a root-spelled hint beside a qualified
// Field until it is taught here.
func requalifyAction(action, origField, qualifiedField string) string {
	if action == "" || origField == "" {
		return action
	}
	lead, body := "", action
	if rest, found := strings.CutPrefix(action, actionEnableLeadIn); found {
		lead, body = actionEnableLeadIn, rest
	}
	key, ok := yamlKeyFromAction(body)
	if !ok || body != missingFieldAction(key) {
		return action
	}
	qualifiedKey, ok := reattachHead(key, origField, qualifiedField)
	if !ok {
		return action
	}
	return lead + missingFieldAction(qualifiedKey)
}

// yamlKeyFromAction reads back the YAML key a generated hint names. Both templates end in
// "add <key> to config.yaml", so the key is what sits between them. The caller still rebuilds
// the hint from the key and compares, which is what proves the text was generated rather than
// merely shaped like it — including that its env half is the one envVarForKey derives.
func yamlKeyFromAction(action string) (string, bool) {
	const addPrefix, yamlSuffix = "add ", " to config.yaml"
	rest, ok := strings.CutSuffix(action, yamlSuffix)
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, addPrefix)
	if i < 0 {
		return "", false
	}
	return rest[i+len(addPrefix):], true
}

// reattachHead moves one dotted key from oldHead to newHead: the head itself becomes newHead, a
// key UNDER it keeps its remainder, and anything else is not oldHead's to move and reports false.
// It is the one place that rule lives — the field qualifiers and the hint re-pointer all read a
// key against a head this way, and the dot is the delimiter each of them measures in, so the
// trap missingFieldAction documents (a dot inside a section or tenant NAME) is one trap here
// rather than one per caller.
func reattachHead(key, oldHead, newHead string) (string, bool) {
	switch {
	case key == oldHead:
		return newHead, true
	case strings.HasPrefix(key, oldHead+"."):
		return newHead + strings.TrimPrefix(key, oldHead), true
	default:
		return "", false
	}
}

// missingFieldAction is the hint NewMissingFieldError builds for key. The env half is
// dropped when no variable reaches the key (see envVarForKey), leaving the YAML path,
// which is always reachable.
//
// The guard covers every non-injective case of the transform EXCEPT a dot inside a section
// or tenant NAME, since the dot is the delimiter the round trip is measured in:
// multitenant.tenants.acme.corp.database.port round-trips cleanly but unflattens to tenant
// "acme", sub-key "corp". No producer can reach that today — koanf cannot deliver a map key
// with an embedded dot, and the connect door raises no missing-field errors — so it is a
// trap for a future caller rather than a live hole. Suppress the env half explicitly if you
// ever raise one of these from a free-form key.
func missingFieldAction(key string) string {
	if envVar := envVarForKey(key); envVar != "" {
		return fmt.Sprintf(actionSetEnvOrYAMLPath, envVar, key)
	}
	return fmt.Sprintf(actionAddYAMLPath, key)
}
