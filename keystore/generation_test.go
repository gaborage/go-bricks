package keystore

import (
	"crypto/rsa"
	"encoding/base64"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func TestValidateLogicalGrammarTable(t *testing.T) {
	tests := []struct {
		name    string
		logical string
		wantErr string
	}{
		{name: "valid_hyphenated", logical: "svc-payments-sign"},
		{name: "valid_underscore_and_upper", logical: "Svc_Payments"},
		{name: "valid_64_chars", logical: strings.Repeat("a", 64)},
		{name: "invalid_65_chars", logical: strings.Repeat("a", 65), wantErr: "is 65 characters, maximum is 64"},
		{name: "invalid_dot", logical: "svc.payments", wantErr: "must match"},
		{name: "invalid_empty", logical: "", wantErr: "must match"},
		{name: "invalid_trailing_generation", logical: "svc-sign-v3", wantErr: "must not end in the generation marker"},
		{name: "valid_v_without_digits", logical: "svc-v"},
		{name: "valid_digits_without_hyphen", logical: "svcv3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLogical(tt.logical)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSplitGeneration(t *testing.T) {
	tests := []struct {
		name        string
		entry       string
		wantLogical string
		wantVersion string
		wantOK      bool
	}{
		{name: "generation_entry", entry: "svc-payments-sign-v2", wantLogical: "svc-payments-sign", wantVersion: "v2", wantOK: true},
		{name: "last_marker_wins", entry: "x-v1-v2", wantLogical: "x-v1", wantVersion: "v2", wantOK: true},
		{name: "bare_marker_empty_logical", entry: "-v1", wantLogical: "", wantVersion: "v1", wantOK: true},
		{name: "leading_zero_is_still_a_marker", entry: "x-v01", wantLogical: "x", wantVersion: "v01", wantOK: true},
		{name: "ordinary_entry", entry: "signing", wantOK: false},
		{name: "v_without_digits_is_ordinary", entry: "svc-v", wantOK: false},
		{name: "digits_without_v_is_ordinary", entry: "svc-2", wantOK: false},
		{name: "uppercase_V_is_ordinary", entry: "svc-V2", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logical, version, ok := splitGeneration(tt.entry)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantLogical, logical)
			assert.Equal(t, tt.wantVersion, version)
		})
	}
}

func TestFamilyOfRefusals(t *testing.T) {
	entry := &keyEntry{public: &rsa.PublicKey{}}
	tests := []struct {
		name    string
		entry   string
		wantErr string
	}{
		{name: "family_ends_in_marker", entry: "x-v1-v2", wantErr: `key "x-v1-v2": logical kid "x-v1" must not end in the generation marker`},
		{name: "leading_zero_version", entry: "x-v01", wantErr: `key "x-v01": generation "v01" must be a positive integer without leading zeros`},
		{name: "zero_version", entry: "x-v0", wantErr: `key "x-v0": generation "v0" must be a positive integer without leading zeros`},
		{name: "empty_family", entry: "-v1", wantErr: `key "-v1": logical kid "" must match`},
		{name: "family_too_long", entry: strings.Repeat("a", 65) + "-v1", wantErr: "is 65 characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok, err := familyOf(tt.entry, entry)
			require.Error(t, err)
			assert.False(t, ok)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestFamilyOfAcceptsCanonicalVersions(t *testing.T) {
	for _, version := range []string{"v1", "v10", "v99999999999999999999"} {
		gen, ok, err := familyOf("fam-"+version, &keyEntry{public: &rsa.PublicKey{}})
		require.NoError(t, err, version)
		require.True(t, ok, version)
		assert.Equal(t, Generation{Logical: "fam", Version: version, Role: RolePublicOnly}, gen)
		assert.Equal(t, "fam-"+version, gen.Kid())
	}
}

func TestFamilyOfOrdinaryEntryIsNotJudged(t *testing.T) {
	// 65 characters and a dot: both would fail the Logical grammar, but an
	// ordinary (HTTP jose) entry name carries no family part to judge.
	for _, name := range []string{strings.Repeat("a", 65), "svc.legacy"} {
		_, ok, err := familyOf(name, &keyEntry{public: &rsa.PublicKey{}})
		assert.NoError(t, err, name)
		assert.False(t, ok, name)
	}
}

func TestRoleOfAndString(t *testing.T) {
	priv := &rsa.PrivateKey{}
	tests := []struct {
		name  string
		entry *keyEntry
		want  Role
		str   string
	}{
		{name: "public_only", entry: &keyEntry{public: &priv.PublicKey}, want: RolePublicOnly, str: "public-only"},
		{name: "private_present", entry: &keyEntry{public: &priv.PublicKey, private: priv}, want: RolePrivate, str: "private"},
		{name: "secret", entry: &keyEntry{secret: []byte("k")}, want: RoleSecret, str: "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roleOf(tt.entry)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.str, got.String())
		})
	}
	assert.Equal(t, "Role(0)", Role(0).String())
}

func TestIndexFamiliesSortsNumericallyAndGroups(t *testing.T) {
	pub := &rsa.PublicKey{}
	priv := &rsa.PrivateKey{}
	entries := map[string]*keyEntry{
		"fam-v10":  {public: pub},
		"fam-v2":   {public: pub, private: priv},
		"fam-v1":   {secret: []byte("k")},
		"other-v1": {public: pub},
		"plain":    {public: pub},
	}
	families, err := indexFamilies(slices.Sorted(maps.Keys(entries)), entries)
	require.NoError(t, err)
	assert.Len(t, families, 2)

	versions := make([]string, 0, 3)
	roles := make([]Role, 0, 3)
	for _, g := range families["fam"] {
		assert.Equal(t, "fam", g.Logical)
		assert.Equal(t, "fam-"+g.Version, g.Kid())
		versions = append(versions, g.Version)
		roles = append(roles, g.Role)
	}
	// Lexical order would put v10 before v2; numeric order must not.
	assert.Equal(t, []string{"v1", "v2", "v10"}, versions)
	assert.Equal(t, []Role{RoleSecret, RolePrivate, RolePublicOnly}, roles)
	assert.Equal(t, []Generation{{Logical: "other", Version: "v1", Role: RolePublicOnly}}, families["other"])
	_, plainIndexed := families["plain"]
	assert.False(t, plainIndexed)
}

// TestCompareGenerationsOrdersByInteger pins integer order where lexical
// order would differ (v2 < v10) and where the two agree (v10 < v11).
func TestCompareGenerationsOrdersByInteger(t *testing.T) {
	g := func(v string) Generation { return Generation{Logical: "f", Version: v} }
	assert.Negative(t, CompareGenerations(g("v2"), g("v10")))
	assert.Positive(t, CompareGenerations(g("v10"), g("v2")))
	assert.Negative(t, CompareGenerations(g("v10"), g("v11")))
	assert.Zero(t, CompareGenerations(g("v7"), g("v7")))
}

func TestIndexFamiliesRefusalNamesTheFirstBadEntry(t *testing.T) {
	entries := map[string]*keyEntry{
		"zz-v01": {public: &rsa.PublicKey{}},
		"aa-v1":  {public: &rsa.PublicKey{}},
	}
	_, err := indexFamilies([]string{"aa-v1", "zz-v01"}, entries)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `key "zz-v01"`)
}

// secretCfg builds a base64 secret source at the default floor so a store can
// be constructed without RSA material.
func secretCfg(fill byte) config.KeyPairConfig {
	raw := make([]byte, config.DefaultKeyStoreSecretMinLength)
	for i := range raw {
		raw[i] = fill
	}
	return config.KeyPairConfig{Secret: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(raw)}}
}

func TestNewStoreRefusesIllegalFamilyAtStartup(t *testing.T) {
	_, err := newStore(map[string]config.KeyPairConfig{
		"svc-sign-v3-v1": secretCfg('a'),
	}, config.DefaultKeyStoreSecretMinLength)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `keystore: key "svc-sign-v3-v1": logical kid "svc-sign-v3" must not end in the generation marker`)
}

func TestNewStoreGenerationsEnumeratesProvisionedOnly(t *testing.T) {
	priv, pub := generateTestKeys(t)
	pubB64 := base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pub))
	privB64 := base64.StdEncoding.EncodeToString(marshalPrivateKeyDER(t, priv))

	s, err := newStore(map[string]config.KeyPairConfig{
		"svc-sign-v1": {Public: config.KeySourceConfig{Value: pubB64}},
		"svc-sign-v2": {Public: config.KeySourceConfig{Value: pubB64}, Private: config.KeySourceConfig{Value: privB64}},
		"svc-mac-v1":  secretCfg('m'),
		"svc-sign":    {Public: config.KeySourceConfig{Value: pubB64}}, // ordinary entry sharing the family's name
	}, config.DefaultKeyStoreSecretMinLength)
	require.NoError(t, err)

	assert.Equal(t, []Generation{
		{Logical: "svc-sign", Version: "v1", Role: RolePublicOnly},
		{Logical: "svc-sign", Version: "v2", Role: RolePrivate},
	}, s.Generations("svc-sign"))
	assert.Equal(t, []Generation{{Logical: "svc-mac", Version: "v1", Role: RoleSecret}}, s.Generations("svc-mac"))
	assert.Empty(t, s.Generations("nobody"))

	// Name-addressed access to generation entries is untouched.
	_, err = s.PrivateKey("svc-sign-v2")
	assert.NoError(t, err)
	_, err = s.PrivateKey("svc-sign-v1")
	assert.Error(t, err)
}

func TestGenerationsReturnsCallerOwnedSlice(t *testing.T) {
	s, err := newStore(map[string]config.KeyPairConfig{
		"fam-v1": secretCfg('a'),
		"fam-v2": secretCfg('b'),
	}, config.DefaultKeyStoreSecretMinLength)
	require.NoError(t, err)

	first := s.Generations("fam")
	first[0].Version = "tampered"
	assert.Equal(t, "fam-v1", s.Generations("fam")[0].Kid())

	var enumerator FamilyEnumerator = s
	assert.Len(t, enumerator.Generations("fam"), 2)
}
