package config

import (
	"encoding/binary"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLenientTrustedProxyKeysRejectEveryDefaultRouteSpelling closes the asymmetry that made the
// debug-endpoint bypass reachable: the SAME value that aborts startup on
// server.trustedproxies was accepted on debug.trustedproxies and
// scheduler.security.trustedproxies, because those two route to the lenient
// validateCIDRList while server routes to the strict ParseTrustedProxyCIDR.
//
// With every peer trusted, an attacker connects DIRECTLY and the forwarding-header
// path opens, which is what turns a header the caller wrote into the address an
// access-control check believes.
func TestLenientTrustedProxyKeysRejectEveryDefaultRouteSpelling(t *testing.T) {
	// Every one of these trusts an entire address family. "127.0.0.1/0" is a default route
	// wearing a host address; "::ffff:0.0.0.0/96" is one the SECURITY AUDIT found walking
	// past a mask-size check, because Mask.Size() reads 96/128 while Contains re-derives a
	// 4-byte mask and matches every IPv4 address. The rule is coverage, not spelling.
	for _, entry := range []string{"0.0.0.0/0", "::/0", "127.0.0.1/0", "2001:db8::1/0", "::ffff:0.0.0.0/96", "::ffff:0:0/96"} {
		t.Run("debug_"+safeSubtestName(entry), func(t *testing.T) {
			cfg := &DebugConfig{TrustedProxies: []string{entry}}
			err := checkDebug(cfg)
			require.Error(t, err, "a default route must not be accepted on debug.trustedproxies")
			assert.Contains(t, err.Error(), "debug.trustedproxies")
			assert.Contains(t, err.Error(), "trusts every address")
		})

		t.Run("scheduler_"+safeSubtestName(entry), func(t *testing.T) {
			cfg := &SchedulerConfig{Security: SchedulerSecurityConfig{TrustedProxies: []string{entry}}}
			err := checkScheduler(cfg)
			require.Error(t, err, "a default route must not be accepted on scheduler.security.trustedproxies")
			assert.Contains(t, err.Error(), "scheduler.security.trustedproxies")
			assert.Contains(t, err.Error(), "trusts every address")
		})
	}
}

// TestDebugAllowedIPsRejectsUnparseableEntries covers a key that had NO CIDR-syntax
// validation: only ADR-078's delivered-empty check ran, so a typo produced a silent
// runtime deny-all rather than a startup error. An operator locked out of their own
// debug endpoints by a malformed entry should be told at boot, not left to infer it.
//
// Bare addresses MUST be accepted: the shipped default is ["127.0.0.1","::1"], which the
// strict proxy parser rejects. This key is an allowlist, not a trust list, so unlike
// trustedproxies it may legitimately admit everything — ADR-049 recommends ["0.0.0.0/0"]
// for it — and a default route here is NOT an error.
func TestDebugAllowedIPsRejectsUnparseableEntries(t *testing.T) {
	t.Run("rejects_a_malformed_entry", func(t *testing.T) {
		cfg := &DebugConfig{AllowedIPs: []string{"127.0.0.1", "not-an-ip"}}
		err := checkDebug(cfg)
		require.Error(t, err, "a malformed allowlist entry must fail at startup")
		assert.Contains(t, err.Error(), "debug.allowedips")
	})

	for _, entry := range []string{"127.0.0.1", "::1", "10.0.0.0/8", "2001:db8::/32", "0.0.0.0/0"} {
		t.Run("accepts_"+safeSubtestName(entry), func(t *testing.T) {
			cfg := &DebugConfig{AllowedIPs: []string{entry}}
			assert.NoError(t, checkDebug(cfg), "%q is a legitimate allowlist entry", entry)
		})
	}
}

// collectKoanfPaths walks a struct tree and returns the dotted koanf path of every field
// whose own koanf tag equals want. It is the discovery half of the trusted-proxy rule:
// the list under test is authoritative only if nothing can exist outside it.
func collectKoanfPaths(t *testing.T, typ reflect.Type, want, prefix string, out *[]string) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("koanf"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		if tag == want {
			*out = append(*out, path)
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			collectKoanfPaths(t, ft, want, path, out)
		}
	}
}

// TestTrustedProxyFieldsCoverEveryKoanfTag is the generalized form of the default-route
// rule: rather than trusting a hand-written list of the keys that exist today, it DISCOVERS
// every `koanf:"trustedproxies"` field in the Config tree and asserts each one is covered.
//
// This is the tripwire the bypass needed and did not have. Three keys meant the same thing,
// two were validated leniently and one strictly, and nothing noticed for two releases. A
// fourth key added to types.go without being wired here fails this test rather than shipping
// the same hole again.
func TestTrustedProxyFieldsCoverEveryKoanfTag(t *testing.T) {
	var found []string
	collectKoanfPaths(t, reflect.TypeOf(Config{}), "trustedproxies", "", &found)

	require.NotEmpty(t, found, "the walk found no trustedproxies keys at all — the walk is broken, not the config")

	listed := make([]string, 0, len(trustedProxyKeys))
	for _, k := range trustedProxyKeys {
		listed = append(listed, k.field)
	}
	assert.ElementsMatch(t, listed, found,
		"every config key named trustedproxies must appear in trustedProxyKeys and reject a default route; "+
			"a key present in the tree but missing from the table is the exact shape of the bypass ADR-080 closes")
}

// TestEveryTrustedProxyKeyRejectsDefaultRoute is the behavioral half: each discovered key,
// set to a default route on an otherwise-valid config, must fail config.Validate. The
// discovery test above proves the list is complete; this proves each member is wired to a
// validator that actually enforces the rule, rather than merely being listed.
func TestEveryTrustedProxyKeyRejectsDefaultRoute(t *testing.T) {
	// The payloads matter as much as the keys. An earlier version of this test used only
	// "0.0.0.0/0" — which the strict server parser already rejected — so it passed while
	// server.trustedproxies still accepted a v4-mapped default route AND a split-coverage
	// pair. A test named for every key must feed every key the shapes that break it.
	payloads := map[string][]string{
		"explicit_default_route": {"0.0.0.0/0"},
		"ipv6_default_route":     {"::/0"},
		"v4_mapped_default":      {"::ffff:0.0.0.0/96"},
		"split_coverage":         {"0.0.0.0/1", "128.0.0.0/1"},
		"three_way_split":        {"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/2"},
	}

	for _, k := range trustedProxyKeys {
		for name, entries := range payloads {
			t.Run(k.field+"_"+name, func(t *testing.T) {
				cfg := createValidFullConfig()
				k.set(cfg, entries)

				err := Validate(cfg)
				require.Error(t, err, "%s must reject %v", k.field, entries)
				// "trusts every address" (one entry) or "together trust every address" (a set).
				assert.Contains(t, err.Error(), "every address")
			})
		}
	}
}

// trustedProxyKeys is every config key that decides WHICH PEERS may set forwarding headers,
// paired with the setter that reaches it. Keeping the name and the setter together means a
// key cannot be listed without being exercised.
//
// The two tests above use it from both directions: one discovers every
// `koanf:"trustedproxies"` field in the Config tree and fails if the table misses any, the
// other drives each entry through config.Validate and fails if the key is wired to a
// validator that does not enforce the rule. Between them, a fourth key cannot reintroduce
// the bypass by being forgotten OR by being wired leniently.
var trustedProxyKeys = []struct {
	field string
	set   func(*Config, []string)
}{
	{fieldServerTrustedProxies, func(c *Config, v []string) { c.Server.TrustedProxies = v }},
	{fieldDebugTrustedProxies, func(c *Config, v []string) { c.Debug.TrustedProxies = v }},
	{fieldSchedulerTrustedProxies, func(c *Config, v []string) { c.Scheduler.Security.TrustedProxies = v }},
}

// safeSubtestName makes a CIDR usable as a subtest name: Go treats "/" as the subtest
// separator, so "0.0.0.0/0" would render as a nested test and break -run targeting.
func safeSubtestName(s string) string {
	return strings.NewReplacer("/", "_", ":", "-").Replace(s)
}

// TestTrustedProxiesRejectTotalCoverageAcrossEntries pins the finding that reopened this
// PR after the first fix: no per-entry rule reaches a trust list that trusts everyone by
// SPLITTING the space. ["0.0.0.0/1","128.0.0.0/1"] is two properly-masked,
// non-default-route entries covering all of IPv4, and it was accepted — then a cross-family
// XFF entry turned it into a grant at both access-control doors.
//
// ADR-080 originally documented that exact list as a safe residual. It was not.
func TestTrustedProxiesRejectTotalCoverageAcrossEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
		reject  bool
	}{
		{name: "two_halves_of_ipv4", entries: []string{"0.0.0.0/1", "128.0.0.0/1"}, reject: true},
		{name: "three_way_split", entries: []string{"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/2"}, reject: true},
		{name: "ipv6_halves", entries: []string{"::/1", "8000::/1"}, reject: true},
		{name: "overlapping_halves", entries: []string{"0.0.0.0/1", "64.0.0.0/2", "128.0.0.0/1"}, reject: true},
		{name: "unordered_entries", entries: []string{"128.0.0.0/1", "0.0.0.0/1"}, reject: true},
		// A gap anywhere means the list does not trust every peer, which is the point of
		// having a list. These must keep working.
		{name: "half_of_ipv4", entries: []string{"0.0.0.0/1"}},
		{name: "rfc1918", entries: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
		{name: "all_but_one_slash32", entries: []string{"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/3", "224.0.0.0/4", "240.0.0.0/5"}},
		{name: "mixed_families_neither_total", entries: []string{"10.0.0.0/8", "2001:db8::/32"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDebug(&DebugConfig{TrustedProxies: tc.entries})
			if !tc.reject {
				assert.NoError(t, err, "a list with a gap trusts fewer than all peers and is legitimate")
				return
			}
			require.Error(t, err, "a list covering an entire address family trusts every peer")
			assert.Contains(t, err.Error(), "trust every address")
		})
	}
}

// TestDebugAllowedIPsRejectsHostBits pins the audit's F5. An allowlist may legitimately
// admit everything, but "192.168.1.55/16" admits 65,536 hosts where the operator wrote one
// address — the same silent widening ParseTrustedProxyCIDR already refuses on the proxy
// keys, in the same words.
func TestDebugAllowedIPsRejectsHostBits(t *testing.T) {
	err := checkDebug(&DebugConfig{AllowedIPs: []string{"192.168.1.55/16"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host bits set")
	assert.Contains(t, err.Error(), "192.168.0.0/16", "the message must name what it actually widens to")

	assert.NoError(t, checkDebug(&DebugConfig{AllowedIPs: []string{"192.168.0.0/16", "127.0.0.1", "0.0.0.0/0"}}),
		"network addresses, bare hosts and a deliberate catch-all stay legal")
}

// mustCIDR parses a CIDR the test author wrote by hand.
func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	require.NoError(t, err, "test CIDR must parse")
	return n
}

// ipv4ExceptLastAddress decomposes 0.0.0.0-255.255.255.254 into the 32 CIDR blocks that
// cover it exactly. It is total coverage minus a single address, built from first
// principles rather than from the function under test.
func ipv4ExceptLastAddress(t *testing.T) []*net.IPNet {
	t.Helper()
	nets := make([]*net.IPNet, 0, 32)
	var start uint32
	for ones := 1; ones <= 32; ones++ {
		ip := make(net.IP, net.IPv4len)
		binary.BigEndian.PutUint32(ip, start)
		nets = append(nets, mustCIDR(t, fmt.Sprintf("%s/%d", ip, ones)))
		start += 1 << (net.IPv4len*8 - ones)
	}
	return nets
}

// TestCoversAddressFamilyBoundaries pins the merge loop directly. Everything the config
// half now refuses rests on this predicate, and a boundary error here is silent in both
// directions: too eager and every legitimate multi-range proxy list is locked out, too
// lax and a set that trusts the whole internet is waved through. The one-address hole and
// the exactly-contiguous seam are the two cases that separate those failures.
func TestCoversAddressFamilyBoundaries(t *testing.T) {
	const v4Bits = net.IPv4len * 8

	t.Run("exactly_contiguous_at_the_seam_is_covered", func(t *testing.T) {
		nets := []*net.IPNet{mustCIDR(t, "0.0.0.0/1"), mustCIDR(t, "128.0.0.0/1")}
		assert.True(t, CoversAddressFamily(nets, v4Bits),
			"two halves meeting with no gap trust every address")
	})

	t.Run("one_address_hole_is_not_covered", func(t *testing.T) {
		assert.False(t, CoversAddressFamily(ipv4ExceptLastAddress(t), v4Bits),
			"255.255.255.255 is untrusted, so the list still distinguishes somebody")
	})

	t.Run("filling_the_one_address_hole_covers", func(t *testing.T) {
		nets := append(ipv4ExceptLastAddress(t), mustCIDR(t, "255.255.255.255/32"))
		assert.True(t, CoversAddressFamily(nets, v4Bits),
			"the same list plus the last address trusts everyone")
	})

	t.Run("nested_range_does_not_invent_a_gap", func(t *testing.T) {
		// 10.0.0.0/8 sits inside 0.0.0.0/1. If the sweep tracked the last endpoint seen
		// instead of the running maximum, reach would fall back to 10.255.255.255 and
		// 128.0.0.0 would read as a gap — accepting a list that trusts everyone.
		nets := []*net.IPNet{
			mustCIDR(t, "0.0.0.0/1"),
			mustCIDR(t, "10.0.0.0/8"),
			mustCIDR(t, "128.0.0.0/1"),
		}
		assert.True(t, CoversAddressFamily(nets, v4Bits),
			"a range nested in an earlier one cannot un-cover what is already covered")
	})
}

// TestDebugAllowedIPsValidationMatchesTheRuntimeParser pins two promises C60.22 makes to
// operators that nothing else covers. Both are about NOT failing a deployment the runtime
// would have served: the runtime parser has always stripped surrounding quotes, so a
// shell-quoting slip that works today must not become a startup failure; and the block is
// validated whether or not debug is enabled, so a typo surfaces at deploy time rather than
// during the incident in which someone flips it on.
func TestDebugAllowedIPsValidationMatchesTheRuntimeParser(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		entries []string
		wantErr bool
	}{
		{name: "double_quoted_entry_is_accepted", enabled: true, entries: []string{`"127.0.0.1"`}},
		{name: "single_quoted_entry_is_accepted", enabled: true, entries: []string{`'127.0.0.1'`}},
		{name: "quoted_cidr_is_accepted", enabled: true, entries: []string{`"10.0.0.0/8"`}},
		{
			name: "malformed_entry_fails_even_when_debug_is_disabled", enabled: false,
			entries: []string{"not-an-ip"}, wantErr: true,
		},
		{name: "valid_entry_passes_when_debug_is_disabled", enabled: false, entries: []string{"127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := createValidFullConfig()
			cfg.Debug.Enabled = tc.enabled
			cfg.Debug.AllowedIPs = tc.entries
			err := Validate(cfg)
			if tc.wantErr {
				require.Error(t, err, "%v must be refused at startup", tc.entries)
				assert.Contains(t, err.Error(), fieldDebugAllowedIPs)
				return
			}
			require.NoError(t, err, "the runtime parser serves %v, so validation must not refuse it", tc.entries)
		})
	}
}
