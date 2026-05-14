package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsDevelopment(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{name: "canonical_development", env: "development", want: true},
		{name: "alias_dev", env: "dev", want: true},
		{name: "alias_local", env: "local", want: true},
		{name: "uppercase_normalized", env: "LOCAL", want: true},
		{name: "mixed_case_normalized", env: "Dev", want: true},
		{name: "surrounding_whitespace", env: "  dev  ", want: true},
		{name: "production_not_dev", env: "production", want: false},
		{name: "prod_alias_not_dev", env: "prd", want: false},
		{name: "staging_neutral", env: "staging", want: false},
		{name: "custom_short_code", env: "tst", want: false},
		{name: "empty", env: "", want: false},
		{name: "garbage", env: "not-a-real-env", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsDevelopment(tc.env))
		})
	}
}

func TestIsProduction(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{name: "canonical_production", env: "production", want: true},
		{name: "alias_prod", env: "prod", want: true},
		{name: "alias_prd", env: "prd", want: true},
		{name: "uppercase_normalized", env: "PRD", want: true},
		{name: "surrounding_whitespace", env: "  production  ", want: true},
		{name: "development_not_prod", env: "development", want: false},
		{name: "dev_alias_not_prod", env: "dev", want: false},
		{name: "local_not_prod", env: "local", want: false},
		{name: "staging_neutral", env: "staging", want: false},
		{name: "custom_short_code", env: "stg", want: false},
		{name: "empty", env: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsProduction(tc.env))
		})
	}
}

func TestAppConfigIsDevelopmentMethod(t *testing.T) {
	tests := []struct {
		name string
		cfg  *AppConfig
		want bool
	}{
		{name: "nil_receiver_safe", cfg: nil, want: false},
		{name: "dev_alias", cfg: &AppConfig{Env: "local"}, want: true},
		{name: "canonical_dev", cfg: &AppConfig{Env: EnvDevelopment}, want: true},
		{name: "neutral_env", cfg: &AppConfig{Env: "stg"}, want: false},
		{name: "production", cfg: &AppConfig{Env: EnvProduction}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cfg.IsDevelopment())
		})
	}
}

func TestAppConfigIsProductionMethod(t *testing.T) {
	tests := []struct {
		name string
		cfg  *AppConfig
		want bool
	}{
		{name: "nil_receiver_safe", cfg: nil, want: false},
		{name: "prod_alias", cfg: &AppConfig{Env: "prd"}, want: true},
		{name: "canonical_prod", cfg: &AppConfig{Env: EnvProduction}, want: true},
		{name: "neutral_env", cfg: &AppConfig{Env: "tst"}, want: false},
		{name: "development", cfg: &AppConfig{Env: EnvDevelopment}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cfg.IsProduction())
		})
	}
}

func TestEnvAliasesAreDisjoint(t *testing.T) {
	for env := range developmentAliases {
		_, collision := productionAliases[env]
		assert.Falsef(t, collision, "%q appears in both developmentAliases and productionAliases", env)
	}
}
