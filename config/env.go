package config

import (
	"regexp"
	"strings"
)

// envFormat catches structural typos in app.env; semantic branching is via
// IsDevelopment / IsProduction. See ADR-021 for the policy rationale.
var envFormat = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

const (
	envAliasDev   = "dev"
	envAliasLocal = "local"
	envAliasProd  = "prod"
	envAliasPrd   = "prd"
)

var (
	developmentAliases = map[string]struct{}{
		EnvDevelopment: {},
		envAliasDev:    {},
		envAliasLocal:  {},
	}
	productionAliases = map[string]struct{}{
		EnvProduction: {},
		envAliasProd:  {},
		envAliasPrd:   {},
	}
)

// envInSet normalizes env (case + surrounding whitespace) so callers that read
// raw env vars before AppConfig validation has run don't need to pre-normalize.
func envInSet(env string, set map[string]struct{}) bool {
	_, ok := set[strings.ToLower(strings.TrimSpace(env))]
	return ok
}

// IsDevelopment reports whether env matches a development alias.
func IsDevelopment(env string) bool { return envInSet(env, developmentAliases) }

// IsProduction reports whether env matches a production alias.
func IsProduction(env string) bool { return envInSet(env, productionAliases) }

// IsDevelopment reports whether a.Env matches a development alias.
func (a *AppConfig) IsDevelopment() bool {
	if a == nil {
		return false
	}
	return IsDevelopment(a.Env)
}

// IsProduction reports whether a.Env matches a production alias.
func (a *AppConfig) IsProduction() bool {
	if a == nil {
		return false
	}
	return IsProduction(a.Env)
}
