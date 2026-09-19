//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/testing/containers"
)

// aclKeyPrefix marks this file's own keys; clusterKeys, which the cluster arm also
// drives through the shared assertions, is client_cluster_integration_test.go's
// fixture data and not this file's to rename.
const aclKeyPrefix = "acl:"

// setupACLRedis leases an ACL-gated container and authenticates as the narrow
// app user the fixture installed.
//
// Everything asserted below rests on the fixture having disabled the implicit
// `default` user (see aclRedisConfig in integration_main_test.go). A stock Redis
// hands an unauthenticated client `default`, which is nopass ~* +@all — against
// that server every round-trip here would succeed whether or not the credential
// ever reached the wire, and the file would prove nothing.
// TestRealRedisACLRejectsBadCredentials is what holds that premise up: its
// no-credentials case must fail with NOAUTH.
func setupACLRedis(t *testing.T, c *containers.RedisContainer, mode string) (*Client, context.Context) {
	t.Helper()

	client, err := NewClient(&Config{
		Host:     c.Host(),
		Port:     c.Port(),
		Mode:     mode,
		PoolSize: 10,
		Username: aclAppUsername,
		Password: aclAppPassword,
	})
	require.NoError(t, err, "the ACL user must be able to connect, PING and read INFO server")
	t.Cleanup(func() { _ = client.Close() })

	return client, t.Context()
}

// TestRealRedisACLUserRoundTrips is the tracer bullet: a real
// AUTH <username> <password> against a server that accepts nothing else, followed
// by the cache operations, so the credential is shown to carry traffic and not
// merely a handshake.
//
// The cluster arm drives the same assertions the open cluster fixture uses
// (assertClusterRoundTrips and assertClusterGetOrSet, in
// client_cluster_integration_test.go): what differs here is the credential the
// client was built with, not what a cluster client is expected to do, so the
// operations are not restated. CompareAndSet is exercised on the standalone arm
// only — it is the one operation with an ACL command check of its own, and the
// grant it needs (+eval) is the same on both.
func TestRealRedisACLUserRoundTrips(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		client, ctx := setupACLRedis(t, pkgRedisACL.Get(t), ModeStandalone)

		const key = aclKeyPrefix + "standalone"
		value := []byte("authenticated-" + key)

		require.NoError(t, client.Set(ctx, key, value, time.Minute))

		got, err := client.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, value, got)

		// CompareAndSet runs as EVAL, where GetOrSet's SET NX GET is still the
		// +set check the plain Set above already made.
		swapped, err := client.CompareAndSet(ctx, key, value, []byte("rotated"), time.Minute)
		require.NoError(t, err)
		assert.True(t, swapped)

		require.NoError(t, client.Delete(ctx, key))
		_, err = client.Get(ctx, key)
		require.ErrorIs(t, err, cache.ErrNotFound)

		assertGetOrSetFromEmptyKey(ctx, t, client, aclKeyPrefix+"standalone:getorset")
	})

	t.Run("cluster", func(t *testing.T) {
		// Not a repeat of the arm above: the credential travels a different copy
		// path. ModeCluster makes NewClient set IsClusterMode, so go-redis reads
		// the credential out of UniversalOptions.Cluster() rather than .Simple(),
		// and cluster discovery runs on this user's connection — CLUSTER SLOTS
		// precedes the first PING, so a credential that failed to reach the
		// cluster path fails construction rather than a later operation.
		//
		// What this arm does NOT prove is that a second dial to a slot-map-
		// discovered node carries the credential: the fixture is one node owning
		// every slot, so the round trip succeeds whether go-redis re-dials the
		// announced address or reuses the seed connection. #1748 covers that.
		client, ctx := setupACLRedis(t, pkgRedisACLCluster.Get(t), ModeCluster)

		assertClusterRoundTrips(ctx, t, client)
		assertGetOrSetFromEmptyKey(ctx, t, client, aclKeyPrefix+"cluster:getorset")
	})
}

// assertGetOrSetFromEmptyKey deletes the key before driving the shared GetOrSet
// assertions. The open fixtures reach those through setupClusterRedis, which flushes
// on entry; the narrow app user holds no FLUSHDB, and the container outlives the test
// binary's individual runs, so a leftover key from a previous -count iteration
// would make wasSet false and fail an assertion that has nothing to do with ACLs.
func assertGetOrSetFromEmptyKey(ctx context.Context, t *testing.T, client *Client, key string) {
	t.Helper()

	require.NoError(t, client.Delete(ctx, key))
	assertClusterGetOrSet(ctx, t, client, key)
}

// TestRealRedisACLUserWithoutInfoStillConstructs pins the fail-open arm wiki/cache.md
// documents for the Redis 7.0 version floor: the check is best-effort and is skipped
// when INFO is unavailable, "ACL-restricted or redacted by a managed provider". The
// unit case that covers it (client_test.go's info_error_fails_open) stubs the
// readServerInfo seam over miniredis, which evaluates no ACL, so this is the first
// time the documented scenario is driven by a server that really refuses.
func TestRealRedisACLUserWithoutInfoStillConstructs(t *testing.T) {
	c := pkgRedisACL.Get(t)

	client, err := NewClient(&Config{
		Host:     c.Host(),
		Port:     c.Port(),
		Mode:     ModeStandalone,
		PoolSize: 10,
		Username: aclNoInfoUsername,
		Password: aclNoInfoPassword,
	})
	require.NoError(t, err, "a user denied INFO must still construct: the version floor fails open")
	t.Cleanup(func() { _ = client.Close() })

	// Without this the test would pass against a user that could read INFO after all,
	// which is the floor being enforced rather than skipped.
	_, err = client.client.Info(t.Context(), "server").Result()
	require.ErrorContains(t, err, "NOPERM")
}

// dialACLIdentity builds a client for one of the fixture's installed identities. The
// guards below each need the same dial under two different passwords, and the failing
// arm needs the error rather than a *testing.T assertion, so construction is not
// wrapped.
func dialACLIdentity(c *containers.RedisContainer, username, password string) (*Client, error) {
	return NewClient(&Config{
		Host:     c.Host(),
		Port:     c.Port(),
		Mode:     ModeStandalone,
		PoolSize: 10,
		Username: username,
		Password: password,
	})
}

// assertOnlyDeclaredPasswordWorks drives both halves of a credential guard against one
// installed identity: the password that must be refused, then the declared one, which
// must still carry a real round trip. The second half is what keeps the first from
// passing against an identity the fixture never installed at all — a name the server
// does not know answers WRONGPASS just the same.
func assertOnlyDeclaredPasswordWorks(t *testing.T, c *containers.RedisContainer, username, declared, refused, refusedMsg string) {
	t.Helper()

	t.Run("refused_password_is_rejected", func(t *testing.T) {
		client, err := dialACLIdentity(c, username, refused)

		require.Error(t, err, refusedMsg)
		assert.Nil(t, client)
		var connErr *cache.ConnectionError
		require.ErrorAs(t, err, &connErr, "a refused credential fails at the dial, not at config validation")
		// The server's own reply: any other failure would mean the identity was
		// never installed, and the refusal would be proving nothing.
		assert.ErrorContains(t, err, "WRONGPASS")
	})

	t.Run("declared_password_still_round_trips", func(t *testing.T) {
		client, err := dialACLIdentity(c, username, declared)
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		ctx := t.Context()
		// The identity's own name keys the round trip: usernames are unique across
		// the fixture, so two guards sharing this helper cannot collide.
		key := aclKeyPrefix + username
		value := []byte("authenticated-" + key)

		require.NoError(t, client.Set(ctx, key, value, time.Minute))
		got, err := client.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, value, got)
		require.NoError(t, client.Delete(ctx, key))
	})
}

// TestRealRedisACLNopassRuleCannotDisableAuthentication holds up the ordering half of
// what redisACLUserArgs settles: the generated `on #<digest>` is emitted AFTER the
// caller's rules, and a FLAG-LIKE rule is a state the last token to set it wins, so a
// caller rule of that family cannot displace it. `nopass` is the worst of them — an
// identity carrying it accepts ANY password, so every credential assertion made
// against it would pass without a credential ever being checked, and the fixture whose
// job is proving authentication would prove nothing. The password family does NOT
// behave this way; TestRealRedisACLCallerPasswordRuleCannotAddACredential covers it.
//
// This is a fixture-vacuity guard rather than a production one: nothing outside this
// test binary writes these rules. It shares pkgRedisACL with the tests above rather
// than booting a server of its own — aclRedisConfig installs the identity as an
// additional user.
func TestRealRedisACLNopassRuleCannotDisableAuthentication(t *testing.T) {
	assertOnlyDeclaredPasswordWorks(t, pkgRedisACL.Get(t),
		aclNopassUsername, aclNopassPassword, aclNopassPassword+"-tampered",
		"the caller's nopass rule must not survive the generated auth state")
}

// TestRealRedisACLCallerPasswordRuleCannotAddACredential holds up the other half, which
// ordering alone does NOT settle. A Redis ACL password is a LIST: `>pw` and `#hash`
// append to it rather than setting a state, so a caller rule carrying a password
// survives the generated `on #<digest>` that follows it and leaves the identity
// accepting two credentials — measured on this image, both AUTH'd. `resetpass`,
// emitted immediately before the generated state, empties that list, and the refused
// arm here is the server-side proof: the password the caller's own rules installed
// must no longer authenticate.
//
// The identity's declared password is a different value from the injected one
// (aclExtraPwPassword vs aclInjectedPassword, asserted distinct in
// TestACLAppRulesStayNarrow), so the accepted arm cannot be satisfied by the very
// credential the refused arm says is gone.
func TestRealRedisACLCallerPasswordRuleCannotAddACredential(t *testing.T) {
	assertOnlyDeclaredPasswordWorks(t, pkgRedisACL.Get(t),
		aclExtraPwUsername, aclExtraPwPassword, aclInjectedPassword,
		"resetpass must clear the password the caller's own rules added")
}

// TestRealRedisACLRejectsBadCredentials is what makes the positives above
// non-vacuous, and it runs against BOTH ACL containers because each installs its
// own boot arguments — a `default` that stayed enabled on one of them would not
// show up in the other's result.
//
// The no_credentials case is the load-bearing one: an empty username with an
// empty password passes Config.validateUsername (the shape rule only fires on a
// username without a password), so the client dials with no AUTH at all. Its
// NOAUTH is the direct evidence that `default` is off, and therefore the license
// for every positive assertion in this file. The other two cases pin that a
// credential which is present but wrong is refused by the server rather than
// quietly downgraded.
func TestRealRedisACLRejectsBadCredentials(t *testing.T) {
	servers := []struct {
		name  string
		mode  string
		lease func(t *testing.T) *containers.RedisContainer
	}{
		{name: "standalone", mode: ModeStandalone, lease: pkgRedisACL.Get},
		{name: "cluster", mode: ModeCluster, lease: pkgRedisACLCluster.Get},
	}

	credentials := []struct {
		name     string
		username string
		password string
		wantErr  string
	}{
		{
			name:     "wrong_password_is_rejected",
			username: aclAppUsername,
			password: aclAppPassword + "-tampered",
			wantErr:  "WRONGPASS",
		},
		{
			name:     "unknown_username_is_rejected",
			username: aclAppUsername + "-does-not-exist",
			password: aclAppPassword,
			wantErr:  "WRONGPASS",
		},
		{
			name:    "no_credentials_is_rejected",
			wantErr: "NOAUTH",
		},
	}

	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			// Leased here rather than in the table literal above, which the parent
			// evaluates: there both containers would boot even when -run selects one
			// arm, and a Docker-unavailable skip would land on the parent instead of
			// on the arm that asked for the container.
			container := server.lease(t)

			for _, tt := range credentials {
				t.Run(tt.name, func(t *testing.T) {
					client, err := NewClient(&Config{
						Host:     container.Host(),
						Port:     container.Port(),
						Mode:     server.mode,
						PoolSize: 10,
						Username: tt.username,
						Password: tt.password,
					})

					require.Error(t, err, "the server accepts nothing unauthenticated")
					assert.Nil(t, client, "NewClient returns no client alongside an error")

					var connErr *cache.ConnectionError
					require.ErrorAs(t, err, &connErr, "a refused credential fails at the dial, not at config validation")
					// The server's own reply, not just "some error": a generic
					// failure would pass here against a server that refused the
					// connection for an unrelated reason.
					assert.ErrorContains(t, err, tt.wantErr)
				})
			}
		})
	}
}
