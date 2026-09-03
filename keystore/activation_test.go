package keystore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// fakeFamilies is a FamilyEnumerator over a literal accept set.
type fakeFamilies map[string][]Generation

func (f fakeFamilies) Generations(logical string) []Generation { return f[logical] }

func gens(logical string, versions ...string) []Generation {
	out := make([]Generation, len(versions))
	for i, v := range versions {
		out[i] = Generation{Logical: logical, Version: v, Role: RolePrivate}
	}
	return out
}

func TestActiveGenerationMatrix(t *testing.T) {
	const fam = "svc-sign"
	tests := []struct {
		name        string
		provisioned []string
		active      map[string]string
		want        string
		wantErr     string
	}{
		{name: "zero_absent", wantErr: `logical kid "svc-sign" has no provisioned generation`},
		{name: "zero_selected", active: map[string]string{fam: "v1"}, wantErr: `logical kid "svc-sign" has no provisioned generation`},
		{name: "one_absent_auto_active", provisioned: []string{"v1"}, want: "v1"},
		{name: "one_valid", provisioned: []string{"v1"}, active: map[string]string{fam: "v1"}, want: "v1"},
		{name: "one_unprovisioned", provisioned: []string{"v1"}, active: map[string]string{fam: "v2"}, wantErr: `messaging.seal.active.svc-sign = "v2" names an unprovisioned generation (provisioned: v1)`},
		{name: "two_absent", provisioned: []string{"v1", "v2"}, wantErr: `has 2 provisioned generations (v1, v2) and no messaging.seal.active.svc-sign selector`},
		{name: "two_valid_picks_named_not_newest", provisioned: []string{"v1", "v2"}, active: map[string]string{fam: "v1"}, want: "v1"},
		{name: "two_valid_newest", provisioned: []string{"v1", "v2"}, active: map[string]string{fam: "v2"}, want: "v2"},
		{name: "two_unprovisioned", provisioned: []string{"v1", "v2"}, active: map[string]string{fam: "v3"}, wantErr: `= "v3" names an unprovisioned generation (provisioned: v1, v2)`},
		{name: "selector_for_other_family_ignored", provisioned: []string{"v1"}, active: map[string]string{"svc-enc": "v9"}, want: "v1"},
		{name: "malformed_selector_named", provisioned: []string{"v1"}, active: map[string]string{fam: "v01"}, wantErr: `messaging.seal.active.svc-sign = "v01" is not a generation`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := fakeFamilies{fam: gens(fam, tt.provisioned...)}
			got, err := ActiveGeneration(store, tt.active, fam)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Equal(t, Generation{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, fam+"-"+tt.want, got.Kid())
			assert.Equal(t, RolePrivate, got.Role)
		})
	}
}

func TestActiveGenerationRejectsIllegalLogical(t *testing.T) {
	store := fakeFamilies{"svc-sign-v3": gens("svc-sign-v3", "v1")}
	_, err := ActiveGeneration(store, nil, "svc-sign-v3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `keystore: logical kid "svc-sign-v3" must not end in the generation marker`)
}

func TestActiveGenerationAgainstRealStore(t *testing.T) {
	s, err := newStore(map[string]config.KeyPairConfig{
		"svc-mac-v1": secretCfg('a'),
		"svc-mac-v2": secretCfg('b'),
	}, config.DefaultKeyStoreSecretMinLength)
	require.NoError(t, err)

	got, err := ActiveGeneration(s, map[string]string{"svc-mac": "v2"}, "svc-mac")
	require.NoError(t, err)
	assert.Equal(t, Generation{Logical: "svc-mac", Version: "v2", Role: RoleSecret}, got)

	_, err = ActiveGeneration(s, nil, "svc-mac")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no messaging.seal.active.svc-mac selector")
}
