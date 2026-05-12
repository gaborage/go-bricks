package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func TestSecretsProviderValidate(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		fetcher SecretFetcher
		wantErr error
	}{
		{
			name:    "empty_prefix_with_fetcher_ok",
			prefix:  "",
			fetcher: func(context.Context, string) ([]byte, error) { return nil, nil },
		},
		{
			name:    "valid_prefix_with_trailing_slash",
			prefix:  "myorg/migrate/",
			fetcher: func(context.Context, string) ([]byte, error) { return nil, nil },
		},
		{
			name:    "missing_fetcher",
			prefix:  "",
			fetcher: nil,
			wantErr: ErrNoFetcher,
		},
		{
			name:    "prefix_without_trailing_slash",
			prefix:  "myorg/migrate",
			fetcher: func(context.Context, string) ([]byte, error) { return nil, nil },
			wantErr: ErrInvalidPrefix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &SecretsProvider{Prefix: tt.prefix, Fetch: tt.fetcher}
			err := p.Validate()
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestSecretsProviderSecretName(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		tenant string
		want   string
	}{
		{name: "default_prefix", prefix: "", tenant: "tenant-a", want: "gobricks/migrate/tenant-a"},
		{name: "custom_prefix", prefix: "prod/migrate/", tenant: "t1", want: "prod/migrate/t1"},
		{name: "preserves_id_verbatim", prefix: "p/", tenant: "Tenant_With/Slash", want: "p/Tenant_With/Slash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &SecretsProvider{Prefix: tt.prefix}
			assert.Equal(t, tt.want, p.SecretName(tt.tenant))
		})
	}
}

func TestSecretsProviderDBConfigCanonical(t *testing.T) {
	payload := []byte(`{
		"type":     "postgresql",
		"host":     "tenant-a.db.example.com",
		"port":     5432,
		"database": "tenant_a",
		"username": "tenant_a_app",
		"password": "s3cret"
	}`)

	p := &SecretsProvider{
		Fetch: func(_ context.Context, name string) ([]byte, error) {
			assert.Equal(t, "gobricks/migrate/tenant-a", name)
			return payload, nil
		},
	}

	got, err := p.DBConfig(context.Background(), "tenant-a")
	require.NoError(t, err)
	assert.Equal(t, &config.DatabaseConfig{
		Type:     "postgresql",
		Host:     "tenant-a.db.example.com",
		Port:     5432,
		Database: "tenant_a",
		Username: "tenant_a_app",
		Password: "s3cret",
	}, got)
}

func TestSecretsProviderDBConfigRDSRotation(t *testing.T) {
	payload := []byte(`{
		"engine":   "postgres",
		"host":     "tenant-b.rds.amazonaws.com",
		"port":     5432,
		"dbname":   "tenant_b",
		"username": "tenant_b_app",
		"password": "rds-rotated"
	}`)

	p := &SecretsProvider{
		Fetch: func(context.Context, string) ([]byte, error) { return payload, nil },
	}

	got, err := p.DBConfig(context.Background(), "tenant-b")
	require.NoError(t, err)
	assert.Equal(t, "postgresql", got.Type)
	assert.Equal(t, "tenant_b", got.Database)
}

func TestSecretsProviderDBConfigCanonicalWinsOverRDS(t *testing.T) {
	payload := []byte(`{
		"type":     "oracle",
		"engine":   "postgres",
		"host":     "h",
		"database": "canonical_db",
		"dbname":   "rds_db",
		"username": "u"
	}`)

	p := &SecretsProvider{
		Fetch: func(context.Context, string) ([]byte, error) { return payload, nil },
	}

	got, err := p.DBConfig(context.Background(), "x")
	require.NoError(t, err)
	assert.Equal(t, "oracle", got.Type)
	assert.Equal(t, "canonical_db", got.Database)
}

func TestSecretsProviderDBConfigEngineNormalization(t *testing.T) {
	cases := map[string]string{
		"postgres":           "postgresql",
		"postgresql":         "postgresql",
		"aurora-postgresql":  "postgresql",
		"oracle":             "oracle",
		"oracle-se2":         "oracle",
		"unknown-engine-xyz": "unknown-engine-xyz",
	}

	for engine, want := range cases {
		t.Run(engine, func(t *testing.T) {
			payload := []byte(`{"engine":"` + engine + `","host":"h","username":"u"}`)
			p := &SecretsProvider{
				Fetch: func(context.Context, string) ([]byte, error) { return payload, nil },
			}
			got, err := p.DBConfig(context.Background(), "x")
			require.NoError(t, err)
			assert.Equal(t, want, got.Type)
		})
	}
}

func TestSecretsProviderDBConfigErrors(t *testing.T) {
	t.Run("empty_payload", func(t *testing.T) {
		p := &SecretsProvider{Fetch: func(context.Context, string) ([]byte, error) { return nil, nil }}
		_, err := p.DBConfig(context.Background(), "x")
		assert.ErrorIs(t, err, ErrSecretMalformed)
	})

	t.Run("invalid_json", func(t *testing.T) {
		p := &SecretsProvider{Fetch: func(context.Context, string) ([]byte, error) { return []byte("{not json"), nil }}
		_, err := p.DBConfig(context.Background(), "x")
		assert.ErrorIs(t, err, ErrSecretMalformed)
	})

	t.Run("missing_required_fields", func(t *testing.T) {
		p := &SecretsProvider{Fetch: func(context.Context, string) ([]byte, error) { return []byte(`{"port":1}`), nil }}
		_, err := p.DBConfig(context.Background(), "x")
		assert.ErrorIs(t, err, ErrSecretMalformed)
	})

	t.Run("fetcher_error_propagates", func(t *testing.T) {
		boom := errors.New("vault unreachable")
		p := &SecretsProvider{Fetch: func(context.Context, string) ([]byte, error) { return nil, boom }}
		_, err := p.DBConfig(context.Background(), "x")
		assert.ErrorIs(t, err, boom)
	})

	t.Run("nil_fetcher", func(t *testing.T) {
		p := &SecretsProvider{}
		_, err := p.DBConfig(context.Background(), "x")
		assert.ErrorIs(t, err, ErrNoFetcher)
	})

	t.Run("error_includes_tenant_and_secret_name", func(t *testing.T) {
		p := &SecretsProvider{
			Prefix: "prod/",
			Fetch:  func(context.Context, string) ([]byte, error) { return []byte("garbage"), nil },
		}
		_, err := p.DBConfig(context.Background(), "tenant-x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tenant-x")
		assert.Contains(t, err.Error(), "prod/tenant-x")
	})
}

func TestSecretsProviderContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &SecretsProvider{
		Fetch: func(ctx context.Context, _ string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []byte(`{}`), nil
		},
	}

	_, err := p.DBConfig(ctx, "x")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSecretsProviderEmptyTenantID(t *testing.T) {
	p := &SecretsProvider{
		Fetch: func(context.Context, string) ([]byte, error) {
			t.Fatal("Fetch should not be called for empty tenantID")
			return nil, nil
		},
	}
	_, err := p.DBConfig(context.Background(), "   ")
	require.ErrorIs(t, err, ErrEmptyTenantID)
}

func TestSecretsProviderTenantIDAllowlist(t *testing.T) {
	validPayload := []byte(`{"type":"postgresql","host":"h","username":"u"}`)

	t.Run("accepts_allowed_characters", func(t *testing.T) {
		cases := []string{
			"tenant1",
			"TENANT-X",
			"a_b-c_2",
			"A",                                // 1 char
			strings.Repeat("a", 128),           // 128 chars (upper bound)
			"abcdef-ABCDEF-0123456789-_______", // mixed allowed runes
		}
		for _, id := range cases {
			t.Run(id, func(t *testing.T) {
				p := &SecretsProvider{
					Fetch: func(context.Context, string) ([]byte, error) { return validPayload, nil },
				}
				_, err := p.DBConfig(context.Background(), id)
				require.NoError(t, err)
			})
		}
	})

	t.Run("rejects_disallowed_inputs", func(t *testing.T) {
		cases := []struct {
			name string
			id   string
		}{
			{"space", "tenant 1"},
			{"slash", "tenant/secret"},
			{"dot", "tenant.id"},
			{"colon", "tenant:id"},
			{"nul", "tenant\x00id"},
			{"newline", "tenant\nid"},
			{"unicode", "tenanté"},
			{"over_128_chars", strings.Repeat("a", 129)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p := &SecretsProvider{
					Fetch: func(context.Context, string) ([]byte, error) {
						t.Fatal("Fetch should not be called for disallowed tenantID")
						return nil, nil
					},
				}
				_, err := p.DBConfig(context.Background(), tc.id)
				require.ErrorIs(t, err, ErrInvalidTenantID)
			})
		}
	})

	t.Run("trims_then_validates", func(t *testing.T) {
		p := &SecretsProvider{
			Fetch: func(context.Context, string) ([]byte, error) { return validPayload, nil },
		}
		// Leading/trailing whitespace is trimmed; the inner ID must still match.
		_, err := p.DBConfig(context.Background(), "  tenant-1  ")
		require.NoError(t, err)
	})
}
