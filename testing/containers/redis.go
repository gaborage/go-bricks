//go:build integration

package containers

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// redisPort is the container-side port every Redis wait strategy and MappedPort lookup
// addresses.
const redisPort = "6379/tcp"

// redisCLI is the in-container client every bootstrap and readiness exec runs.
const redisCLI = "redis-cli"

// redisTerminateTimeout bounds the teardown of a container whose startup
// failed, independent of the caller's (likely expired) startup deadline.
const redisTerminateTimeout = 30 * time.Second

// Container-side paths the TLS material is copied to. They match the layout the
// upstream redis module uses, so a reader comparing the two sees one scheme.
const (
	redisTLSCAPath   = "/tls/ca.crt"
	redisTLSCertPath = "/tls/server.crt"
	redisTLSKeyPath  = "/tls/server.key"
)

// Cluster bootstrap knobs. StartupTimeout bounds the cluster-ready wait as well
// as the container start — each leg separately, not the two together, so a
// cluster fixture's worst case is twice the configured value and must still fit
// the caller's own budget (the Shared deadline in integration_main_test.go).
const (
	redisClusterReadyState    = "cluster_state:ok"
	redisClusterBootstrapPoll = 250 * time.Millisecond
)

// redisTLSFileMode makes the copied PEM world-readable: redis-server drops to
// an unprivileged user before opening them, so 0o600 owned by root would make
// the server fail to start rather than fail to verify.
const redisTLSFileMode = 0o644

// RedisTLSMaterial is the PEM material a TLS-only Redis container serves: the
// certificate and key it presents, and the CA it is signed by (which is also
// what a client must trust).
type RedisTLSMaterial struct {
	CA   []byte
	Cert []byte
	Key  []byte
}

// RedisACLUser is one Redis ACL identity: the name a client presents, the
// password it authenticates with, and the rules it is granted. Empty Rules take
// the fixture's default for the slot the user occupies — redisACLAdminRules for
// Admin, redisACLDefaultAppRules for App and AdditionalUsers — so a caller that
// names none still gets a narrow user rather than having to invent a rule list.
type RedisACLUser struct {
	Username string
	Password string
	Rules    []string
}

// RedisACL gates the container behind Redis ACLs. Disabling the implicit
// `default` user is the whole point: left enabled it is `nopass ~* +@all`, so an
// unauthenticated client works and a credential test passes whether or not the
// credential ever traveled. With it off, every connection must authenticate.
//
// Admin is what the fixture's own execs run as and App is the credential under
// test; why the two are granted so differently is on the rule vars below.
//
// A password here reaches the server as a SHA-256 rule, so the boot argument a
// failing server prints to its log — which testcontainers dumps to the test output
// — carries a digest rather than the credential. The clear-text value still
// travels: it is what a client under test authenticates with, and what the
// fixture's own redis-cli execs pass as `--pass`. Only throwaway values belong in
// these fields.
type RedisACL struct {
	Admin RedisACLUser
	App   RedisACLUser
	// AdditionalUsers installs further named identities alongside App, each with
	// its own Rules, for a test that needs a second credential with different
	// grants on the same server.
	AdditionalUsers []RedisACLUser
}

// redisACLUserFlag is the boot argument that opens every `--user` directive;
// it appears in the default-off directive, in each installed identity, and in
// the authenticated redis-cli prefix.
const redisACLUserFlag = "--user"

// redisACLDefaultUsername is Redis' implicit user. Every ACL-gated fixture turns
// it off, so it is also the one name a fixture identity may not claim: declaring
// it twice is what makes the server reject the whole directive at boot.
const redisACLDefaultUsername = "default"

// redisACLAdminRules make the admin identity a fixture superuser: it exists to
// run the bootstrap chores — CONFIG SET, CLUSTER ADDSLOTSRANGE, CLUSTER INFO —
// not to be exercised.
var redisACLAdminRules = []string{"~*", "&*", "+@all"}

// redisACLDefaultAppRules is what an App or additional user naming no Rules is
// granted: narrow on purpose, because an app user holding the admin commands
// would be a superuser under another name and a test authenticating as it would
// demonstrate less. The keyspace stays `~*` because this fixture proves credential
// authentication, not key-pattern enforcement; the command list is what keeps the
// user from being an admin alias, and it holds neither CONFIG nor FLUSHDB. The
// per-grant derivation lives with the package that measured it, in
// cache/redis/integration_main_test.go.
var redisACLDefaultAppRules = []string{
	"~*",
	"+ping",
	"+info",
	"+get",
	"+set",
	"+del",
	"+eval",
	"+command",
	"+cluster|slots",
}

// RedisContainerConfig holds configuration for Redis test container
type RedisContainerConfig struct {
	// ImageTag specifies the Redis version (default: the pin in DefaultRedisConfig)
	ImageTag string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
	// Cluster starts the node with --cluster-enabled yes and gives it every one
	// of the 16384 slots, so one server answers for the whole keyspace and a
	// cluster-protocol client has a single endpoint to talk to — the CLIENT-side
	// shape an Amazon ElastiCache Serverless deployment requires. It is not that
	// service's topology: serverless shards and redirects (ADR-117), and neither
	// is reproduced here. Composes with TLS: the two arms contribute separate
	// flags to one command line, and the bootstrap then speaks TLS to the node
	// it just started.
	Cluster bool
	// TLS, when non-nil, makes the server TLS-only: the plaintext listener is
	// disabled and TLS is bound to 6379 instead, so Host() and Port() address
	// the TLS listener exactly as they address the plaintext one — a caller only
	// has to turn TLS on client-side. Nil (the default) serves plaintext. Under
	// Cluster the fixture's own bootstrap dials that same listener from inside
	// the container, verifying it against the same CA.
	TLS *RedisTLSMaterial
	// ACL, when non-nil, disables the implicit `default` user and installs the
	// identities it names, so the server accepts nothing unauthenticated.
	// Nil (the default) leaves the stock open server every other fixture uses.
	// Composes with Cluster and TLS exactly as those two compose with each
	// other: each arm contributes its own flags to the one command line, and the
	// fixture's own redis-cli then carries the transport flags and the admin
	// credential together on the one invocation.
	ACL *RedisACL
}

// DefaultRedisConfig returns a RedisContainerConfig populated with sensible defaults.
//
// The returned configuration sets StartupTimeout to 60 seconds.
func DefaultRedisConfig() *RedisContainerConfig {
	return &RedisContainerConfig{
		// renovate: datasource=docker depName=redis
		ImageTag:       "8.10.2-alpine",
		StartupTimeout: 60 * time.Second,
	}
}

// RedisContainer wraps testcontainers Redis container with helper methods
type RedisContainer struct {
	container *redis.RedisContainer
	host      string
	port      int
}

// StartRedisContainer starts a Redis testcontainer using the provided configuration.
// If cfg is nil, DefaultRedisConfig is used. If Docker is not available the test is
// skipped with a clear message. On success it returns a RedisContainer wrapping the
// running container and its connection details; on failure it returns a non-nil error.
func StartRedisContainer(ctx context.Context, t *testing.T, cfg *RedisContainerConfig) (*RedisContainer, error) {
	t.Helper()

	if cfg == nil {
		cfg = DefaultRedisConfig()
	}

	if !isDockerAvailable(ctx) {
		t.Skip(DockerUnavailableSkipMessage)
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	cc, err := startRedisContainerInternal(ctx, cfg)
	if err != nil {
		return nil, err
	}

	t.Logf("Redis container started successfully at %s:%d", cc.host, cc.port)

	return cc, nil
}

// StartRedisContainerForTestMain starts a Redis test container without
// requiring a *testing.T. Intended for package-level TestMain usage where
// container provisioning must happen before m.Run() and *T is unavailable.
//
// Returns (container, true, nil) on success.
// Returns (nil, false, nil) when Docker is unavailable — what that means is the
// caller's decision: a package whose tests are all integration tests may log and
// os.Exit(0), while a package that also holds unit tests hands the tuple to
// containers.Shared, which skips only the requesting test.
// Returns (nil, true, err) when Docker is available but startup failed.
//
// Callers are responsible for invoking Terminate after m.Run().
func StartRedisContainerForTestMain(ctx context.Context, cfg *RedisContainerConfig) (container *RedisContainer, dockerAvailable bool, err error) {
	if !isDockerAvailable(ctx) {
		return nil, false, nil
	}
	cc, err := startRedisContainerInternal(ctx, cfg)
	if err != nil {
		return nil, true, err
	}
	return cc, true, nil
}

// redisOptions builds the container customizers, wait strategy included.
//
// Composite wait strategy: log message (fast early signal) + port listening (network
// verification) prevents a race where the log appears but Redis is not ready to accept
// connections.
//
// Cluster, TLS and ACL each contribute to one accumulated argument list rather
// than owning the command, so a fixture asking for all three gets all three.
// WithCmdArgs (not
// WithCmd) is what the upstream module uses for its own TLS mode: the image's
// entrypoint prepends `redis-server` to any argument list starting with a dash,
// so passing bare flags replaces the default command without naming the binary
// twice. It APPENDS to the request's command, which is why arms compose at all,
// why collecting every arm's flags into one call reads the same as several, and
// why the plain standalone case needs no guard: appending nothing leaves the
// image's own command exactly as it was.
//
// Each arm owns every flag it is responsible for, so `--tls-cluster yes` sits in
// the cluster arm: it configures the CLUSTER BUS, and without it the bus port
// derives from the disabled plaintext port (measured: bus 10000 rather than
// tls-port + 10000). Nothing here exercises that bus — one node owning every
// slot never dials a peer — so removing the flag was measured NOT to fail a
// test. It is correct for a real cluster and unproven by this fixture; treat it
// as load-bearing anyway.
//
// `--port 0` disables the plaintext listener while TLS binds the same 6379, so
// the listening-port wait still holds. Client certificates are not required
// (`--tls-auth-clients no`): the fixture exercises SERVER verification, and no
// client under test presents one. Demanding a client cert would redden the
// POSITIVE tests — the handshakes that are meant to succeed — not the negative
// ones, which already fail client-side (unknown authority, hostname mismatch)
// before the server's demand is ever reached.
//
// The ACL arm comes last, so a reader comparing a gated fixture's command with an
// open one sees the credential directives appended to an otherwise identical
// line. redisACLArgs says why they are boot arguments rather than post-start
// ACL SETUSER execs.
func redisOptions(cfg *RedisContainerConfig) []testcontainers.ContainerCustomizer {
	opts := make([]testcontainers.ContainerCustomizer, 0, 3)
	opts = append(opts, waitOptionWithin(cfg.StartupTimeout,
		wait.ForLog("Ready to accept connections"),
		wait.ForListeningPort(redisPort),
	))

	args := make([]string, 0, 16)
	if cfg.Cluster {
		args = append(args, "--cluster-enabled", "yes")
		if cfg.TLS != nil {
			args = append(args, "--tls-cluster", "yes")
		}
	}
	if cfg.TLS != nil {
		opts = append(opts, testcontainers.WithFiles(redisTLSFiles(cfg.TLS)...))
		args = append(args,
			"--tls-port", strings.TrimSuffix(redisPort, "/tcp"),
			"--port", "0",
			"--tls-cert-file", redisTLSCertPath,
			"--tls-key-file", redisTLSKeyPath,
			"--tls-ca-cert-file", redisTLSCAPath,
			"--tls-auth-clients", "no",
		)
	}
	if cfg.ACL != nil {
		args = append(args, redisACLArgs(cfg.ACL)...)
	}

	return append(opts, testcontainers.WithCmdArgs(args...))
}

// redisACLArgs renders cfg.ACL as boot arguments: the implicit `default` user
// turned off first, then one directive per installed identity.
//
// Boot arguments rather than post-start ACL SETUSER execs: a malformed rule
// fails the server's startup outright, and the server never accepts a connection
// during a window in which the ACL is not yet installed. Redis refuses to mix
// `--user` directives with an `--aclfile`, so this fixture uses only the former.
// No shell is involved (testcontainers passes argv straight through), so
// `#<digest>` and `~*` need no quoting.
func redisACLArgs(acl *RedisACL) []string {
	// Admin first so the fixture's own execs have an identity, then App, then
	// any additional identity a test installs. Capacity is an estimate: three
	// tokens for the default-off directive, then five plus a rule list each.
	args := make([]string, 0, 3+(2+len(acl.AdditionalUsers))*(5+len(redisACLDefaultAppRules)))
	args = append(args, redisACLUserFlag, redisACLDefaultUsername, "off")
	args = append(args, redisACLUserArgs(acl.Admin, redisACLAdminRules)...)
	for _, u := range slices.Concat([]RedisACLUser{acl.App}, acl.AdditionalUsers) {
		args = append(args, redisACLUserArgs(u, redisACLDefaultAppRules)...)
	}
	return args
}

// validateRedisACLUsernames refuses a user set the server would reject while
// parsing its own boot arguments. That rejection is what puts a directive in a log
// at all: Redis echoes the whole offending `--user` directive and exits, and
// testcontainers dumps that log to stderr when the readiness wait fails, where go
// test folds it into the CI job log. The password rule in it is a SHA-256 digest
// (redisACLUserArgs), so this check removes the trigger while the hashed form
// removes the payload — and it names the fixture's own bug instead of leaving a
// readiness timeout to explain. A bad *rule* prints only the rule token; a
// duplicate or reserved *username* prints the directive. redisACLArgs renders
// arguments with no error channel, so the check lives on the start path, before
// anything starts.
func validateRedisACLUsernames(acl *RedisACL) error {
	seen := make(map[string]struct{}, 2+len(acl.AdditionalUsers))
	for _, u := range slices.Concat([]RedisACLUser{acl.Admin, acl.App}, acl.AdditionalUsers) {
		// Whitespace ANYWHERE is refused with the empty case, not just a
		// whitespace-only name: Redis quotes each argv element before parsing, so
		// `user " " on …` and `user "a b" on …` both install rather than failing at
		// boot, and yield an identity no client can sensibly present — while the
		// server's own ACL-file grammar splits the same directive on whitespace. NUL
		// joins them: it terminates the C string the server compares against, so a
		// name carrying one is not the name the caller wrote. This is wider than
		// cache/redis's own Config.validateUsername, which refuses only the
		// whitespace-only form because an AUTH argument is not a config token.
		switch {
		case u.Username == "" || strings.ContainsFunc(u.Username, func(r rune) bool { return unicode.IsSpace(r) || r == 0 }):
			return errors.New("redis container: every ACL user needs a username, and one of Admin, App or AdditionalUsers has none, has only whitespace, or carries whitespace or a NUL inside it")
		case u.Password == "":
			// Not a shape nit: sha256("") is a perfectly valid rule, so the
			// identity installs and authenticates with the empty string. A
			// `+@all` Admin with an empty credential boots a server every ACL
			// assertion still passes against, which is the vacuity this fixture
			// exists to prevent. Whitespace is a legitimate password, so only
			// the empty one is refused.
			return fmt.Errorf("redis container: ACL user %q needs a password; an empty one installs sha256(\"\") and authenticates with the empty string", u.Username)
		case u.Username == redisACLDefaultUsername:
			return fmt.Errorf("redis container: ACL username %q is the implicit user the fixture disables; declaring it again makes Redis refuse the directive at boot", u.Username)
		}
		if _, dup := seen[u.Username]; dup {
			return fmt.Errorf("redis container: ACL username %q is declared twice, and Redis refuses a duplicate user declaration at boot", u.Username)
		}
		seen[u.Username] = struct{}{}
	}
	return nil
}

// redisACLUserArgs renders one
// `--user <name> <rules...> resetpass on #<sha256-of-password>` directive, falling
// back to the fixture's default rules for a user that names none. The generated auth
// state trails the caller's rules, and `resetpass` sits immediately in front of it,
// so the identity requires exactly the password it was declared with.
//
// Two mechanisms, because Redis treats the two rule families differently. A FLAG-LIKE
// rule — `nopass`, `off`, `reset` — sets a state the last token to touch it wins, so
// position alone overrides a caller rule that would have disabled authentication.
// (`reset` also wipes the key patterns and command grants that preceded it, and
// nothing below restores those: such an identity authenticates and then gets NOPERM
// on its first operation — loud, and the direction a fixture should fail in.) A
// PASSWORD rule is not a flag: `>pw` and `#hash` APPEND to the identity's password
// list, so a caller's own password form survives whatever follows it. Measured on
// this image, `--user probe ">otherpw" … on "#<digest>"` left BOTH passwords
// authenticating — a second valid credential on an identity this fixture claims has
// one. `resetpass` empties that list (and clears `nopass` with it) just before the
// generated state is applied, so only the digest below remains. Neither mechanism
// enumerates rules, so neither has a token to miss.
//
// The hashed rule form rather than `>password`: the directive is a boot argument a
// failing server can echo to a log the test output captures, and a digest there is
// not a credential a reader can replay. Redis hashes what a client AUTHs with and
// compares, so the client still presents the clear-text password.
func redisACLUserArgs(u RedisACLUser, fallback []string) []string {
	rules := u.Rules
	if len(rules) == 0 {
		rules = fallback
	}
	digest := sha256.Sum256([]byte(u.Password))
	args := make([]string, 0, 5+len(rules))
	args = append(args, redisACLUserFlag, u.Username)
	args = append(args, rules...)
	return append(args, "resetpass", "on", "#"+hex.EncodeToString(digest[:]))
}

// redisTLSFiles is the PEM material copied into a TLS-only container, each at
// the path its own command flag names.
func redisTLSFiles(material *RedisTLSMaterial) []testcontainers.ContainerFile {
	files := make([]testcontainers.ContainerFile, 0, 3)
	for _, f := range []struct {
		path string
		pem  []byte
	}{
		{redisTLSCAPath, material.CA},
		{redisTLSCertPath, material.Cert},
		{redisTLSKeyPath, material.Key},
	} {
		files = append(files, testcontainers.ContainerFile{
			Reader:            bytes.NewReader(f.pem),
			ContainerFilePath: f.path,
			FileMode:          redisTLSFileMode,
		})
	}
	return files
}

// startRedisContainerInternal does the actual testcontainer setup without
// any *testing.T interaction. Both StartRedisContainer (which adds *T-bound
// Skip/Logf) and StartRedisContainerForTestMain wrap it.
func startRedisContainerInternal(ctx context.Context, cfg *RedisContainerConfig) (*RedisContainer, error) {
	if cfg == nil {
		cfg = DefaultRedisConfig()
	}

	if cfg.ACL != nil {
		if err := validateRedisACLUsernames(cfg.ACL); err != nil {
			return nil, err
		}
	}

	// redis.Run can hand back a started container together with an error (a
	// wait-strategy timeout, say), so terminate it before returning — the same
	// contract newRedisContainer honors for its own later failures.
	redisContainer, err := redis.Run(ctx,
		fmt.Sprintf("redis:%s", cfg.ImageTag),
		redisOptions(cfg)...,
	)
	if err != nil {
		if redisContainer != nil {
			terminateOnFailure(ctx, redisContainer)
		}
		return nil, fmt.Errorf("failed to start Redis container: %w", err)
	}

	c, err := newRedisContainer(ctx, redisContainer)
	if err != nil {
		return nil, err
	}
	if !cfg.Cluster {
		return c, nil
	}
	if err := bootstrapRedisCluster(ctx, c, cfg); err != nil {
		terminateOnFailure(ctx, redisContainer)
		return nil, err
	}
	return c, nil
}

// bootstrapRedisCluster turns a freshly started cluster-enabled node into a
// one-node cluster that owns every slot. The node starts owning none and reports
// cluster_state:fail until the claim lands and the cluster cron has run, so the
// claim is followed by a wait rather than assumed.
//
// The announce pair is what makes it reachable: the slot map a cluster client
// follows carries each node's OWN address, which inside Docker is a container IP
// and port no host-side client can dial. Announcing the mapped host address
// makes the node advertise where the test actually reaches it, so the client's
// first redirect lands rather than hangs.
//
// WHICH port key carries it depends on the listener the client arrived on: a
// node serves a TLS caller the TLS port and a plaintext caller the plaintext
// one, from two separate settings, so the TLS arm sets cluster-announce-tls-port
// and nothing else. wiki/testing.md records the ablation that established it.
func bootstrapRedisCluster(ctx context.Context, c *RedisContainer, cfg *RedisContainerConfig) error {
	announceIP, err := resolveAnnounceIP(ctx, c.host)
	if err != nil {
		return err
	}

	announcePortKey := "cluster-announce-port"
	if cfg.TLS != nil {
		announcePortKey = "cluster-announce-tls-port"
	}

	for _, args := range [][]string{
		{"config", "set", "cluster-announce-ip", announceIP},
		{"config", "set", announcePortKey, strconv.Itoa(c.port)},
		{"cluster", "addslotsrange", "0", "16383"},
	} {
		if err := execRedisCLI(ctx, c, cfg, args...); err != nil {
			return err
		}
	}

	return waitForRedisClusterReady(ctx, c, cfg)
}

// redisCLICommand builds one in-container redis-cli invocation.
//
// `-e` is the flag that makes an exit status worth reading: by default redis-cli
// exits 0 on a command the server refused, and with -e an error reply exits
// non-zero instead. Every invocation carries it, plaintext and TLS alike, so
// both doors inherit the detection — execRedisCLI from its own exit-code check,
// and wait.ForExec for free, since its default ExitCodeMatcher already
// requires 0.
//
// A TLS-only node has no plaintext listener left for the bootstrap to arrive on,
// so the admin path speaks TLS itself, and it verifies: --cacert pins the same
// CA the client under test trusts, and nothing here weakens that — --insecure
// and its kin never appear. Hostname verification is not part of what redis-cli
// does here (measured: it validates the chain, not the name), so the fixture's
// leaf need not cover the address redis-cli dials.
//
// On an ACL-gated node the `default` user is off, so an unauthenticated bootstrap
// or readiness exec is refused and the cluster never reaches cluster_state:ok —
// these run as the admin identity. The credential arm is here rather than in a
// second builder precisely so it cannot miss a transport arm: a container that is
// cluster AND TLS AND ACL needs the CA flags and the credential on the SAME
// invocation, and two builders could each serve only half of it.
// --no-auth-warning suppresses the stderr notice redis-cli prints for a
// command-line password, which Multiplexed() folds into the same buffer as the
// command's own output.
func redisCLICommand(cfg *RedisContainerConfig, args ...string) []string {
	cmd := make([]string, 0, len(args)+10)
	cmd = append(cmd, redisCLI, "-e")
	if cfg.TLS != nil {
		cmd = append(cmd, "--tls", "--cacert", redisTLSCAPath)
	}
	if cfg.ACL != nil {
		cmd = append(cmd, redisACLUserFlag, cfg.ACL.Admin.Username, "--pass", cfg.ACL.Admin.Password, "--no-auth-warning")
	}
	return append(cmd, args...)
}

// withoutRedisCLICredential strips the admin password out of text built from a
// full redis-cli argv. It is the only secret redisCLICommand puts there.
func withoutRedisCLICredential(cfg *RedisContainerConfig, text string) string {
	if cfg.ACL == nil || cfg.ACL.Admin.Password == "" {
		return text
	}
	return strings.ReplaceAll(text, cfg.ACL.Admin.Password, "<redacted>")
}

// execRedisCLI builds one in-container redis-cli command and runs it: the single
// door for the bootstrap, so that no call site can build a command and forget to
// run it through the builder — which on a TLS-only node would silently exec a
// plaintext redis-cli against a listener that no longer exists.
//
// A non-zero exit is the whole verdict, because redisCLICommand passes -e. The
// error names the LOGICAL command rather than the built line: the transport
// flags are fixture-chosen and add nothing to a diagnosis, while the credential
// arm's flags would turn an exec failure into a credential leak. Multiplexed
// strips the Docker stream framing, so the message quotes what redis-cli printed
// rather than the wire bytes.
//
// Naming the logical command is what keeps the argv out; withoutRedisCLICredential
// is the second half, over the two texts this function does not author — the
// exec error, which carries whatever the daemon reported, and redis-cli's own
// output. %s rather than %w on the first of those: wrapping would re-embed the
// unredacted original instead of the redacted rendering.
func execRedisCLI(ctx context.Context, c *RedisContainer, cfg *RedisContainerConfig, args ...string) error {
	code, reader, err := c.container.Exec(ctx, redisCLICommand(cfg, args...), tcexec.Multiplexed())
	if err != nil {
		return fmt.Errorf("redis container: exec %v: %s", args, withoutRedisCLICredential(cfg, err.Error()))
	}
	var out bytes.Buffer
	if _, copyErr := out.ReadFrom(reader); copyErr != nil {
		return fmt.Errorf("redis container: reading output of %v: %w", args, copyErr)
	}
	if code != 0 {
		return fmt.Errorf("redis container: %v exited %d: %s", args, code,
			withoutRedisCLICredential(cfg, strings.TrimSpace(out.String())))
	}
	return nil
}

// resolveAnnounceIP turns the host side of the mapped address into something
// cluster-announce-ip will take. Host() is not always an IP literal — a TCP
// Docker endpoint or TESTCONTAINERS_HOST_OVERRIDE hands back a name — and some
// Redis builds reject a hostname there outright, which would fail the bootstrap
// at its first CONFIG SET. IPv4 only: the announced address is what a host-side
// client dials back, and the fixture publishes a v4 mapping — so an IPv6 literal
// is refused here rather than announced into a slot map no redirect could reach.
func resolveAnnounceIP(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		ip4 := ip.To4()
		if ip4 == nil {
			return "", fmt.Errorf("redis container: %q is an IPv6 literal, and cluster-announce-ip needs the IPv4 address the fixture maps", host)
		}
		return ip4.String(), nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", fmt.Errorf("redis container: resolving %q for cluster-announce-ip: %w", host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("redis container: %q has no IPv4 address to announce as cluster-announce-ip", host)
	}
	return ips[0].String(), nil
}

// clusterReadyTimeout is the deadline the cluster-ready wait runs under: the
// caller's StartupTimeout, or the package default when the caller left it zero.
//
// WithStartupTimeout stores a POINTER, so a zero is honored rather than falling
// back to the library default: the wait context would expire before the first
// poll and report a timeout no amount of waiting could have avoided. Only a nil
// cfg gets DefaultRedisConfig, so a caller that builds RedisContainerConfig by
// hand reaches here with zero.
func clusterReadyTimeout(cfg *RedisContainerConfig) time.Duration {
	return cmp.Or(cfg.StartupTimeout, DefaultRedisConfig().StartupTimeout)
}

// waitForRedisClusterReady waits until the node reports the whole slot space as
// served. The state flips on the cluster cron, whose timing is the server's
// business, so this polls rather than sleeps — through the library's own exec
// strategy, the same post-start readiness seam enableStreamPlugin uses, rather
// than a hand-rolled deadline loop. The strategy reports only that it timed out,
// so the last CLUSTER INFO it saw is carried out alongside it.
func waitForRedisClusterReady(ctx context.Context, c *RedisContainer, cfg *RedisContainerConfig) error {
	timeout := clusterReadyTimeout(cfg)

	var last string
	strategy := wait.ForExec(redisCLICommand(cfg, "cluster", "info")).
		WithResponseMatcher(func(body io.Reader) bool {
			out, err := io.ReadAll(body)
			if err != nil {
				return false
			}
			last = strings.TrimSpace(string(out))
			return strings.Contains(last, redisClusterReadyState)
		}).
		WithStartupTimeout(timeout).
		WithPollInterval(redisClusterBootstrapPoll)

	if err := strategy.WaitUntilReady(ctx, c.container); err != nil {
		// %s, not %w: the strategy holds the full redis-cli argv, so wrapping would
		// re-embed the unredacted original instead of the redacted rendering.
		return fmt.Errorf("redis container: cluster did not reach %s within %s (%s); last CLUSTER INFO: %s",
			redisClusterReadyState, timeout, withoutRedisCLICredential(cfg, err.Error()),
			withoutRedisCLICredential(cfg, last))
	}
	return nil
}

// terminateOnFailure tears down a container whose startup failed. The
// caller's ctx is usually the reason it failed (a startup deadline), so the
// teardown runs on a context that keeps its values but not its cancellation,
// bounded on its own. The result is ignored: the startup error is the one to
// report.
func terminateOnFailure(ctx context.Context, c *redis.RedisContainer) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisTerminateTimeout)
	defer cancel()
	_ = c.Terminate(cleanupCtx)
}

// newRedisContainer resolves the host-side address of a started container and
// wraps it. On any lookup failure it terminates the container it was handed —
// the caller never receives a half-built wrapper it would have to clean up.
func newRedisContainer(ctx context.Context, redisContainer *redis.RedisContainer) (*RedisContainer, error) {
	host, err := redisContainer.Host(ctx)
	if err != nil {
		terminateOnFailure(ctx, redisContainer)
		return nil, fmt.Errorf("failed to get Redis host: %w", err)
	}

	mappedPort, err := redisContainer.MappedPort(ctx, redisPort)
	if err != nil {
		terminateOnFailure(ctx, redisContainer)
		return nil, fmt.Errorf("failed to get Redis port: %w", err)
	}

	return &RedisContainer{
		container: redisContainer,
		host:      host,
		port:      int(mappedPort.Num()),
	}, nil
}

// Host returns the container host
func (r *RedisContainer) Host() string {
	return r.host
}

// Port returns the host-side port Docker mapped to the container's 6379.
func (r *RedisContainer) Port() int {
	return r.port
}

// Terminate stops and removes the Redis container
func (r *RedisContainer) Terminate(ctx context.Context) error {
	if r.container == nil {
		return nil
	}
	return r.container.Terminate(ctx)
}

// MustStartRedisContainer starts a Redis test container and fails the test if startup fails.
//
// It is a convenience wrapper around StartRedisContainer that calls t.Fatalf on any error and
// returns the started *RedisContainer when successful.
func MustStartRedisContainer(ctx context.Context, t *testing.T, cfg *RedisContainerConfig) *RedisContainer {
	t.Helper()

	container, err := StartRedisContainer(ctx, t, cfg)
	if err != nil {
		t.Fatalf("Failed to start Redis container: %v", err)
	}

	return container
}

// WithCleanup registers a cleanup function to terminate the container when the test finishes.
// Uses a 30-second timeout to prevent hanging if Docker misbehaves during teardown.
func (r *RedisContainer) WithCleanup(t *testing.T) *RedisContainer {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.Terminate(ctx); err != nil {
			t.Logf("Warning: failed to terminate Redis container: %v", err)
		}
	})
	return r
}
