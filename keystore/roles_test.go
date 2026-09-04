package keystore

import (
	"encoding/base64"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
)

// TestRoleLogReportsOnlyEntriesUnderBothTags varies the overlap: one tag never
// reports, two tags report once per entry with the tags sorted, and the report
// is keyed by entry so two overlaps are two rows.
func TestRoleLogReportsOnlyEntriesUnderBothTags(t *testing.T) {
	cases := []struct {
		name    string
		record  [][2]string
		want    map[string][]string
		entries int
	}{
		{name: "nothing_recorded", want: map[string][]string{}},
		{name: "jose_only", record: [][2]string{{"api-sign", RoleTagJoseRoute}}, want: map[string][]string{}},
		{name: "seal_only", record: [][2]string{{"svc-sign-v1", RoleTagSeal}, {"aud-enc-v1", RoleTagSeal}}, want: map[string][]string{}},
		{
			name: "both_on_one_entry", record: [][2]string{{"shared", RoleTagSeal}, {"shared", RoleTagJoseRoute}, {"api-sign", RoleTagJoseRoute}},
			want: map[string][]string{"shared": {RoleTagJoseRoute, RoleTagSeal}}, entries: 1,
		},
		{
			name: "both_on_two_entries", record: [][2]string{{"b", RoleTagJoseRoute}, {"a", RoleTagSeal}, {"a", RoleTagJoseRoute}, {"b", RoleTagSeal}},
			want: map[string][]string{"a": {RoleTagJoseRoute, RoleTagSeal}, "b": {RoleTagJoseRoute, RoleTagSeal}}, entries: 2,
		},
		{name: "same_tag_twice_is_one_tag", record: [][2]string{{"shared", RoleTagSeal}, {"shared", RoleTagSeal}}, want: map[string][]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log roleLog
			for _, r := range tc.record {
				log.RecordResolution(r[0], r[1])
			}
			got := log.DualRoleEntries()
			assert.Equal(t, tc.want, got)
			assert.Len(t, got, tc.entries)
		})
	}
}

// TestRoleLogIsSafeUnderConcurrentInit pins the lock: module Inits may tag the
// store from several goroutines, and every tag must land exactly once.
func TestRoleLogIsSafeUnderConcurrentInit(t *testing.T) {
	var log roleLog
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			role := RoleTagSeal
			if i%2 == 0 {
				role = RoleTagJoseRoute
			}
			log.RecordResolution("shared", role)
			log.RecordResolution("solo", RoleTagSeal)
		}()
	}
	wg.Wait()
	assert.Equal(t, map[string][]string{"shared": {RoleTagJoseRoute, RoleTagSeal}}, log.DualRoleEntries())
}

// TestStoreCarriesTheRoleLog pins that the module's store is both doors, so the
// resolvers and the app find them by type assertion on app.KeyStore.
func TestStoreCarriesTheRoleLog(t *testing.T) {
	s := &store{}
	var rec RoleRecorder = s
	var rep DualRoleReporter = s
	rec.RecordResolution("k", RoleTagJoseRoute)
	rec.RecordResolution("k", RoleTagSeal)
	assert.Equal(t, map[string][]string{"k": {RoleTagJoseRoute, RoleTagSeal}}, rep.DualRoleEntries())
}

// TestJoseRoutePolicyAndSealTagMeetOnARealStore pins the cross-package tag: the
// "jose-route" string jose.ResolvePolicy records (spelled inside jose, which cannot
// import this package) must be the one RoleTagJoseRoute names, or a shared kid would
// never be reported. A real store, the real resolver, one policy, one seal tag.
func TestJoseRoutePolicyAndSealTagMeetOnARealStore(t *testing.T) {
	priv, pub := generateTestKeys(t)
	s, err := newStore(map[string]config.KeyPairConfig{
		"shared": {
			Public:  config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(marshalPublicKeyDER(t, pub))},
			Private: config.KeySourceConfig{Value: base64.StdEncoding.EncodeToString(marshalPrivateKeyDER(t, priv))},
		},
	}, 0)
	require.NoError(t, err)

	resolver := jose.NewKeyStoreResolver(s)
	require.NoError(t, jose.ResolvePolicy(resolver, &jose.Policy{Direction: jose.DirectionOutbound, SignKid: "shared", EncryptKid: "shared"}))
	assert.Empty(t, s.DualRoleEntries(), "an HTTP-only kid is never reported")

	s.RecordResolution("shared", RoleTagSeal)
	assert.Equal(t, map[string][]string{"shared": {RoleTagJoseRoute, RoleTagSeal}}, s.DualRoleEntries())
}
