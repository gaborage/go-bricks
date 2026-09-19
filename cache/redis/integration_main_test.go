//go:build integration

package redis

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gbtesting "github.com/gaborage/go-bricks/testing"
	"github.com/gaborage/go-bricks/testing/containers"
)

// redisLogicalDatabases is how many logical databases a stock Redis server
// serves (0-15) — the same range Config.Validate accepts.
const redisLogicalDatabases = 16

// redisDBCounter hands out those indices round-robin, one per setupRealRedis
// call, so tests get separate keyspaces on the one shared server.
var redisDBCounter atomic.Int32

// pkgRedis holds the single Redis container this package's test binary shares
// (ADR-020). Each test gets its own logical database, flushed on entry by
// setupRealRedis, rather than a fresh server. Starting lazily keeps a
// unit-test-only run from booting Redis at all.
var pkgRedis = containers.NewShared("Redis", 3*time.Minute,
	func(ctx context.Context) (*containers.RedisContainer, bool, error) {
		return containers.StartRedisContainerForTestMain(ctx, nil)
	})

// pkgRedisCluster holds the single cluster-enabled Redis container this
// package's test binary shares, mirroring pkgRedis. One node owns all 16384
// slots, so a cluster-protocol client has a single endpoint to talk to — the
// client-side shape an Amazon ElastiCache Serverless deployment requires. It is
// not that service's topology: serverless shards and redirects (ADR-117), and a
// one-node fixture reproduces neither. It boots lazily, so a run with no cluster
// test pays nothing for it.
var pkgRedisCluster = containers.NewShared("Redis cluster", 3*time.Minute,
	func(ctx context.Context) (*containers.RedisContainer, bool, error) {
		cfg := containers.DefaultRedisConfig()
		cfg.Cluster = true
		return containers.StartRedisContainerForTestMain(ctx, cfg)
	})

// The ACL identities the ACL-gated fixtures install. They are fake by
// construction: they exist only inside a container this test binary starts and
// terminates, and no deployment ever sees them.
const (
	aclAdminUsername   = "gb-fixture-admin"
	aclAppUsername     = "gb-cache-app"
	aclNoInfoUsername  = "gb-cache-no-info"
	aclNopassUsername  = "gb-cache-nopass"
	aclExtraPwUsername = "gb-cache-extra-password"
)

var (
	aclAdminPassword   = gbtesting.FakePassword("redis-acl-admin")
	aclAppPassword     = gbtesting.FakePassword("redis-acl-app")
	aclNoInfoPassword  = gbtesting.FakePassword("redis-acl-no-info")
	aclNopassPassword  = gbtesting.FakePassword("redis-acl-nopass")
	aclExtraPwPassword = gbtesting.FakePassword("redis-acl-extra-pw")
	// aclInjectedPassword is never declared as an identity's Password: it reaches
	// the server only through a caller RULE, which is the credential the fixture
	// must not leave behind.
	aclInjectedPassword = gbtesting.FakePassword("redis-acl-injected")
)

// aclBadCredentials is the credential set both ACL negative tests drive —
// TestRealRedisACLRejectsBadCredentials and
// TestRealRedisClusterTLSACLRejectsBadCredentials. Only the expectations are
// shared: what each refusal licenses differs per file, so the assertion messages
// stay with their own test.
var aclBadCredentials = []struct{ name, username, password, wantErr string }{
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

// aclAppRules is the narrowest grant set that carries everything this package's
// Client does, established by running the suite against each grant removed:
//
//	+ping             NewClient's connectivity check, and Client.Health
//	+info             readServerInfo's 7.0 version floor, and Client.Stats
//	+get +set +del    Get/Set/Delete, plus the SET NX GET behind GetOrSet
//	+eval             CompareAndSet and CompareAndDelete are Lua
//	+command          go-redis asks for key positions once per call; denied, it
//	                  still works, but each denial lands in the server's fixed-size
//	                  ACL LOG ring and go-redis logs a line of its own
//	+cluster|slots    the cluster client reads the slot map before its first PING
//	+cluster|keyslot  no client operation needs it; the cluster round-trip's
//	                  slot-spread assertion does
var aclAppRules = []string{
	"~*",
	"+ping",
	"+info",
	"+get",
	"+set",
	"+del",
	"+eval",
	"+command",
	"+cluster|slots",
	"+cluster|keyslot",
}

// aclNoInfoRules is aclAppRules with INFO taken away — the shape an ACL-restricted
// or managed endpoint leaves the client in, and the only way to reach the version
// floor's documented fail-open arm against a server that really refuses.
var aclNoInfoRules = slices.DeleteFunc(slices.Clone(aclAppRules), func(rule string) bool { return rule == "+info" })

// aclNopassRules is aclAppRules with `nopass` in front of it — a caller rule that
// makes the identity accept ANY password, and the one the container fixture must not
// let displace the `on #<digest>` it generates (redisACLUserArgs emits that last for
// exactly this reason). The app grants trail it so the accepted-credential half of
// TestRealRedisACLNopassRuleCannotDisableAuthentication can drive a real round trip
// rather than stopping at AUTH.
var aclNopassRules = slices.Concat([]string{"nopass"}, aclAppRules)

// aclExtraPwRules is aclAppRules with a caller-supplied PASSWORD rule in front of it.
// Position does not settle this family the way it settles `nopass`: Redis APPENDS
// `>pw` to the identity's password list, so the generated `on #<digest>` that follows
// adds a second valid credential instead of replacing the first. The `resetpass`
// redisACLUserArgs emits just before that state is what clears it, and
// TestRealRedisACLCallerPasswordRuleCannotAddACredential is the server-side proof.
// The app grants trail the rule so that test's accepted-credential half can drive a
// real round trip rather than stopping at AUTH.
var aclExtraPwRules = slices.Concat([]string{">" + aclInjectedPassword}, aclAppRules)

// TestACLAppRulesStayNarrow keeps the credential under test from becoming an admin
// alias: granted +@all it would authenticate and then pass every assertion in
// client_acl_integration_test.go for a reason that has nothing to do with its own
// grants.
func TestACLAppRulesStayNarrow(t *testing.T) {
	assert.NotContains(t, aclAppRules, "+@all")
	assert.NotContains(t, aclNoInfoRules, "+info", "the fail-open arm rests on INFO being denied")
	assert.Len(t, aclNoInfoRules, len(aclAppRules)-1, "nothing but +info comes out")
	// Without the rule that would disable authentication, the ordering guard
	// degrades into an ordinary user rejecting an ordinary wrong password.
	assert.Contains(t, aclNopassRules, "nopass", "the ordering guard rests on the caller rule being present")
	// Same for the resetpass guard: without the caller's own password rule there
	// is no second credential for resetpass to clear.
	assert.Contains(t, aclExtraPwRules, ">"+aclInjectedPassword, "the resetpass guard rests on the caller password rule being present")
	assert.NotEqual(t, aclExtraPwPassword, aclInjectedPassword, "the two arms must be distinguishable credentials")
}

// aclIdentityDefinitionFile is the one test source in this package allowed to name
// the fixture superuser: it is where the identities are declared. Every other
// *_test.go here is scanned for it.
const aclIdentityDefinitionFile = "integration_main_test.go"

// TestACLIntegrationTestNeverAuthenticatesAsAdmin guards what TestACLAppRulesStayNarrow
// cannot. The ACL tests prove the narrow app user carries the whole cache surface; that
// proof rests entirely on them authenticating AS that user. The admin identity holds
// `~* &* +@all` on the same host-mapped port and is one package-level identifier away, so
// a test reaching for it would pass every assertion for reasons unrelated to the grants
// those tests exist to prove. Source-level because the identity a test picks leaves no
// runtime trace to assert on.
//
// Every test source in the package is scanned rather than one named file: hardcoding a
// filename leaves an ACL test added in a new file silently unguarded, which is exactly
// the future case this guard is for.
func TestACLIntegrationTestNeverAuthenticatesAsAdmin(t *testing.T) {
	sources, err := filepath.Glob("*_test.go")
	require.NoError(t, err)
	require.NotEmpty(t, sources, "the package's own test sources must be readable from the test's working directory")

	narrowIdentityFiles := 0
	for _, path := range sources {
		if path == aclIdentityDefinitionFile {
			continue
		}

		source, err := os.ReadFile(path)
		require.NoError(t, err)
		text := string(source)

		if strings.Contains(text, "aclAppUsername") {
			narrowIdentityFiles++
		}
		for _, needle := range []string{"aclAdminUsername", "aclAdminPassword", aclAdminUsername} {
			assert.NotContains(t, text, needle,
				"%s must not reach for the fixture superuser", path)
		}
	}

	// A renamed, deleted or truncated ACL test would satisfy every NotContains above
	// without anything having been proved.
	require.Positive(t, narrowIdentityFiles,
		"at least one scanned test must still authenticate as the narrow app user")
}

// aclRedisConfig builds an ACL-gated container config. The fixture disables the
// implicit `default` user, so nothing reaches these servers without a credential
// — which is what makes the positive assertions in client_acl_integration_test.go
// mean anything.
func aclRedisConfig(cluster bool) *containers.RedisContainerConfig {
	cfg := containers.DefaultRedisConfig()
	cfg.Cluster = cluster
	cfg.ACL = &containers.RedisACL{
		Admin: containers.RedisACLUser{Username: aclAdminUsername, Password: aclAdminPassword},
		App:   containers.RedisACLUser{Username: aclAppUsername, Password: aclAppPassword, Rules: aclAppRules},
		AdditionalUsers: []containers.RedisACLUser{
			{Username: aclNoInfoUsername, Password: aclNoInfoPassword, Rules: aclNoInfoRules},
			{Username: aclNopassUsername, Password: aclNopassPassword, Rules: aclNopassRules},
			{Username: aclExtraPwUsername, Password: aclExtraPwPassword, Rules: aclExtraPwRules},
		},
	}
	return cfg
}

// pkgRedisACL and pkgRedisACLCluster hold the ACL-gated containers, one per
// protocol: the shared UniversalOptions assignment carries the credentials to a
// single server by itself, but only the cluster client proves they also reach
// the nodes discovered from the slot map. Both boot lazily, like their open
// counterparts, so a run touching neither pays nothing.
var pkgRedisACL = containers.NewShared("Redis ACL", 3*time.Minute,
	func(ctx context.Context) (*containers.RedisContainer, bool, error) {
		return containers.StartRedisContainerForTestMain(ctx, aclRedisConfig(false))
	})

var pkgRedisACLCluster = containers.NewShared("Redis ACL cluster", 3*time.Minute,
	func(ctx context.Context) (*containers.RedisContainer, bool, error) {
		return containers.StartRedisContainerForTestMain(ctx, aclRedisConfig(true))
	})

// tlsRedisContainer carries the CA the TLS tests must trust alongside the
// container that presents it: Shared hands back exactly one value, and the CA
// is minted inside the start func, so it travels with the container. The LEAF
// travels too, because a test whose argument rests on which names the
// certificate covers has to be able to read them back — see
// requireLeafCoversOnlyPinnedName.
type tlsRedisContainer struct {
	*containers.RedisContainer
	caPEM   []byte
	certPEM []byte
}

// pkgRedisTLS holds the single TLS-only Redis container this package's test
// binary shares, mirroring pkgRedis. It boots lazily too, so a run with no TLS
// test pays nothing for it.
var pkgRedisTLS = containers.NewShared("Redis TLS", 3*time.Minute,
	func(ctx context.Context) (*tlsRedisContainer, bool, error) {
		return startTLSRedisContainer(ctx, containers.DefaultRedisConfig(), gbtesting.DefaultCertHosts()...)
	})

// pkgRedisClusterTLS holds the single cluster-enabled, TLS-only Redis container
// this package's test binary shares: the cluster protocol with encryption in
// transit turned on, which is the pair an ElastiCache Serverless client has to
// speak. Its sharding and its redirects are still absent (ADR-117); only the
// client-side shape is reproduced. Lazy like the rest, so a run with no
// cluster-TLS test pays nothing.
//
// Its leaf covers clusterTLSServerName and NOTHING else, deliberately — see
// setupClusterTLSRedis for what a certificate covering the announced address
// too would cost the assertion, and requireLeafCoversOnlyPinnedName for the
// guard that holds this line to it.
var pkgRedisClusterTLS = containers.NewShared("Redis cluster TLS", 3*time.Minute,
	func(ctx context.Context) (*tlsRedisContainer, bool, error) {
		cfg := containers.DefaultRedisConfig()
		cfg.Cluster = true
		return startTLSRedisContainer(ctx, cfg, clusterTLSServerName)
	})

// pkgRedisClusterTLSACL holds the container carrying all three legs at once —
// cluster protocol, TLS-only listener, ACL-gated identities — which is the shape
// an ElastiCache Serverless endpoint presents. Each leg is proven alone by the
// fixtures above; what only this one can show is a slot-map-discovered node
// being re-dialed with BOTH the transport config and the credential, since that
// second dial is the single place the two have to travel together.
//
// It composes from the two seams the other fixtures already established rather
// than declaring the triple itself: aclRedisConfig sets Cluster and ACL,
// startTLSRedisContainer adds the TLS material. Its leaf covers
// clusterTLSServerName and nothing else, for the reason pkgRedisClusterTLS
// documents. Lazy like the rest.
var pkgRedisClusterTLSACL = containers.NewShared("Redis cluster TLS ACL", 3*time.Minute,
	func(ctx context.Context) (*tlsRedisContainer, bool, error) {
		return startTLSRedisContainer(ctx, aclRedisConfig(true), clusterTLSServerName)
	})

// startTLSRedisContainer mints a throwaway CA and a leaf covering certHosts,
// attaches them to the caller's cfg, starts the container and hands the CA and
// the leaf back alongside it — the clients under test have no other way to
// trust it, and the SAN set is a premise a test may need to assert on. The
// caller shapes cfg, so what TLS composes with stays its decision.
func startTLSRedisContainer(ctx context.Context, cfg *containers.RedisContainerConfig, certHosts ...string) (*tlsRedisContainer, bool, error) {
	caPEM, certPEM, keyPEM, err := gbtesting.NewCAAndServerCertPEM(certHosts...)
	if err != nil {
		return nil, true, err
	}

	cfg.TLS = &containers.RedisTLSMaterial{CA: caPEM, Cert: certPEM, Key: keyPEM}

	c, dockerAvailable, err := containers.StartRedisContainerForTestMain(ctx, cfg)
	if c == nil {
		return nil, dockerAvailable, err
	}
	return &tlsRedisContainer{RedisContainer: c, caPEM: caPEM, certPEM: certPEM}, dockerAvailable, err
}

// TestMain terminates the shared containers after the whole binary has run. It
// never exits early when Docker is missing: the package's unit tests still run,
// and each integration test skips itself when it leases its container, inside
// containers.Shared.Get — reached through setupRealRedis for the plaintext
// tests and through the setup helpers of the cluster, TLS, cluster-TLS and
// ACL-gated ones.
func TestMain(m *testing.M) {
	code := m.Run()
	pkgRedis.Close()
	pkgRedisTLS.Close()
	pkgRedisCluster.Close()
	pkgRedisClusterTLS.Close()
	pkgRedisACL.Close()
	pkgRedisACLCluster.Close()
	pkgRedisClusterTLSACL.Close()
	os.Exit(code)
}
