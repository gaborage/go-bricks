//go:build integration

package redis

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
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

	cfg := &Config{
		Host:     c.Host(),
		Port:     c.Port(),
		Mode:     mode,
		PoolSize: 10,
		Username: aclAppUsername,
		Password: aclAppPassword,
	}
	auditCommandDenials(t, cfg)

	client, err := NewClient(cfg)
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
// operations are not restated. Both arms then run the script, health and stats
// paths, and the audit setupACLRedis registers fails the test on any command
// denial the server logged for the app user meanwhile, other than aclWithheldObject.
func TestRealRedisACLUserRoundTrips(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		container := pkgRedisACL.Get(t)
		client, ctx := setupACLRedis(t, container, ModeStandalone)

		const key = aclKeyPrefix + "standalone"
		value := []byte("authenticated-" + key)

		require.NoError(t, client.Set(ctx, key, value, time.Minute))

		got, err := client.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, value, got)

		require.NoError(t, client.Delete(ctx, key))
		_, err = client.Get(ctx, key)
		require.ErrorIs(t, err, cache.ErrNotFound)

		assertGetOrSetFromEmptyKey(ctx, t, client, aclKeyPrefix+"standalone:getorset")
		assertScriptHealthAndStats(ctx, t, client, aclKeyPrefix+"standalone:lease")
	})

	t.Run("cluster", func(t *testing.T) {
		// Not a repeat of the arm above: the credential travels a different copy
		// path. ModeCluster makes NewClient set IsClusterMode, so go-redis reads
		// the credential out of UniversalOptions.Cluster() rather than .Simple(),
		// and cluster discovery runs on this user's connection — the client reads
		// the slot map before the first PING, so a credential that failed to reach
		// the cluster path fails construction rather than a later operation.
		//
		// What this arm does NOT prove is that a second dial to a slot-map-
		// discovered node carries the credential: the fixture is one node owning
		// every slot, so the round trip succeeds whether go-redis re-dials the
		// announced address or reuses the seed connection. #1748 covers that.
		container := pkgRedisACLCluster.Get(t)
		client, ctx := setupACLRedis(t, container, ModeCluster)

		assertClusterRoundTrips(ctx, t, client)
		assertGetOrSetFromEmptyKey(ctx, t, client, aclKeyPrefix+"cluster:getorset")
		assertScriptHealthAndStats(ctx, t, client, aclKeyPrefix+"cluster:lease")
	})
}

// assertScriptHealthAndStats drives the operations the round trips leave out:
// CompareAndSet and CompareAndDelete (EVAL), Health (PING) and Stats (INFO).
func assertScriptHealthAndStats(ctx context.Context, t *testing.T, client *Client, key string) {
	t.Helper()

	value := []byte("held-" + key)
	require.NoError(t, client.Set(ctx, key, value, time.Minute))

	swapped, err := client.CompareAndSet(ctx, key, value, []byte("renewed"), time.Minute)
	require.NoError(t, err)
	assert.True(t, swapped)

	released, err := client.CompareAndDelete(ctx, key, []byte("renewed"))
	require.NoError(t, err)
	assert.True(t, released)

	require.NoError(t, client.Health(ctx))

	_, err = client.Stats()
	require.NoError(t, err)
}

// aclWithheldObject is the one optional grant aclAppRules leaves out on purpose
// (wiki/cache.md): go-redis tolerates its denial, so its ACL LOG entry is expected.
const aclWithheldObject = "client|setinfo"

// auditCommandDenials clears the ACL LOG of the server cfg reaches and, when the
// test ends, fails on any command denial logged for the app user other than
// aclWithheldObject. Call it before building the client under test, so the
// commands construction sends are audited too. The log is read from the seed
// node alone, which covers every command only on the single-node fixtures here,
// and only for the paths the test exercises.
func auditCommandDenials(t *testing.T, cfg *Config) {
	t.Helper()

	reader := resetACLLog(t, cfg)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()

		entries, err := readACLLog(ctx, reader)
		if err != nil {
			t.Error(err)
			return
		}
		for _, e := range entries {
			if e.Username != aclAppUsername || e.Reason != "command" || e.Object == aclWithheldObject {
				continue
			}
			t.Errorf("ACL LOG denied %q to %s %d time(s): grant it in aclAppRules (integration_main_test.go), "+
				"redisACLDefaultAppRules and its pinned expectation (testing/containers), wiki/cache.md and wiki/testing.md",
				e.Object, e.Username, e.Count)
		}
	})
}

// aclLogControlObject is the one command the log reader is refused: issued right
// after the reset, its denial is the entry readACLLog must find before an absence
// of app-user entries can mean the app user was refused nothing. PING exists on
// every server version the suite runs, unlike the withheld CLIENT SETINFO.
const aclLogControlObject = "ping"

// resetACLLog clears the ACL LOG of the node cfg seeds and then has the log
// reader provoke the control denial readACLLog requires. The reset scopes each
// verdict to its own test, which holds only while no ACL test runs in parallel.
func resetACLLog(t *testing.T, cfg *Config) *redis.Client {
	t.Helper()

	reader := newACLLogReader(t, cfg)
	t.Cleanup(func() { _ = reader.Close() })
	require.NoError(t, reader.ACLLogReset(t.Context()).Err(), "the log reader must be able to reset the ACL LOG")
	require.ErrorContains(t, reader.Ping(t.Context()).Err(), "NOPERM",
		"the control denial rests on aclLogRules refusing %s", aclLogControlObject)
	return reader
}

// readACLLog returns the ACL LOG entries recorded since resetACLLog, or an error
// when the log does not hold the reader's control denial: a log that cannot
// show that one cannot show the app user's either.
func readACLLog(ctx context.Context, reader *redis.Client) ([]*redis.ACLLogEntry, error) {
	entries, err := reader.ACLLog(ctx, 128).Result()
	if err != nil {
		return nil, fmt.Errorf("reading the ACL LOG: %w", err)
	}
	for _, e := range entries {
		if e.Username == aclLogUsername && e.Reason == "command" && e.Object == aclLogControlObject {
			return entries, nil
		}
	}
	return nil, fmt.Errorf("the audit could not see the ACL LOG: the control denial of %q to %s is missing from %d entries",
		aclLogControlObject, aclLogUsername, len(entries))
}

// newACLLogReader dials the node cfg seeds, through the same address and TLS
// builders as the client under test, as the identity that may only read the ACL
// LOG. DisableIdentity keeps it from sending the CLIENT SETINFO that one grant
// would refuse.
func newACLLogReader(t *testing.T, cfg *Config) *redis.Client {
	t.Helper()

	material := cfg.TLS.material()
	opts, err := buildRedisOptions(cfg, &material)
	require.NoError(t, err)

	return redis.NewClient(&redis.Options{
		Addr:            opts.Addrs[0],
		TLSConfig:       opts.TLSConfig,
		Username:        aclLogUsername,
		Password:        aclLogPassword,
		DisableIdentity: true,
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

// TestRealRedisACLSlotsOnlyUserFallsBackToClusterSlots pins what wiki/cache.md says
// of a cluster user holding +cluster|slots without +cluster|shards: it still works,
// but pays the denial cost. Every other identity that builds a cluster client holds
// both, so this is the one test that drives go-redis's CLUSTER SLOTS fallback, and with it the reason
// aclAppRules keeps +cluster|slots.
func TestRealRedisACLSlotsOnlyUserFallsBackToClusterSlots(t *testing.T) {
	c := pkgRedisACLCluster.Get(t)

	cfg := &Config{
		Host:     c.Host(),
		Port:     c.Port(),
		Mode:     ModeCluster,
		PoolSize: 10,
		Username: aclSlotsOnlyUsername,
		Password: aclSlotsOnlyPassword,
	}
	reader := resetACLLog(t, cfg)

	client, err := NewClient(cfg)
	require.NoError(t, err, "refused CLUSTER SHARDS, the cluster client must build its slot map from CLUSTER SLOTS")
	t.Cleanup(func() { _ = client.Close() })

	ctx := t.Context()
	const key = aclKeyPrefix + "slots-only"
	value := []byte("authenticated-" + key)
	require.NoError(t, client.Set(ctx, key, value, time.Minute))
	got, err := client.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, value, got)
	require.NoError(t, client.Delete(ctx, key))

	// Without the denial on record the client could have read CLUSTER SHARDS after
	// all, and the round trip above would say nothing about the fallback.
	entries, err := readACLLog(ctx, reader)
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(entries, func(e *redis.ACLLogEntry) bool {
		return e.Username == aclSlotsOnlyUsername && e.Reason == "command" && e.Object == "cluster|shards"
	}), "the server must have logged the refused CLUSTER SHARDS for %s", aclSlotsOnlyUsername)
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
// empty password passes the redisrules username rule (it only fires on a
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

	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			// Leased here rather than in the table literal above, which the parent
			// evaluates: there both containers would boot even when -run selects one
			// arm, and a Docker-unavailable skip would land on the parent instead of
			// on the arm that asked for the container.
			container := server.lease(t)

			for _, tt := range aclBadCredentials {
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
