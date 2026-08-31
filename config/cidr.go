package config

import (
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
)

// validateTrustedProxyList is the whole rule for a LENIENT trusted-proxy key: refuse a
// default route outright, then require that not every remaining entry is unparseable.
// Both keys that use it must apply both halves, so they share one call rather than two
// copies of the pair (server.trustedproxies answers to the stricter
// validateServerTrustedProxies instead).
func validateTrustedProxyList(field string, list []string) error {
	if err := rejectTotalCoverage(field, list); err != nil {
		return err
	}
	return validateCIDRList(field, list)
}

// newDefaultRouteError builds the refusal all three trusted-proxy keys share. The message
// is composed from errTrustedProxyDefaultRoute rather than restating it, so the wording
// cannot drift between the strict server path and the two lenient ones.
func newDefaultRouteError(field, entry string) *ConfigError {
	return &ConfigError{
		Category: errCategoryInvalid,
		Field:    field,
		Message:  fmt.Sprintf("'%s' %s", entry, errTrustedProxyDefaultRoute),
		Action:   actionListSpecificProxyRanges,
	}
}

// NormalizeIPNet returns a net's address and mask size as the family net.IPNet.Contains
// will actually use them. It exists because Mask.Size() and Contains disagree on a
// v4-mapped IPv6 net: "::ffff:0.0.0.0/96" measures 96 of 128 bits, but Contains re-derives
// a 4-byte mask and matches every IPv4 address — so a mask-size test reads it as a /96
// while it behaves as 0.0.0.0/0. Measuring the wrong one is how a default route walks past
// a default-route check.
func NormalizeIPNet(n *net.IPNet) (ip net.IP, ones, bits int) {
	if v4 := n.IP.To4(); v4 != nil {
		mask := n.Mask
		if len(mask) == net.IPv6len {
			mask = mask[12:]
		}
		o, b := mask.Size()
		return v4, o, b
	}
	o, b := n.Mask.Size()
	return n.IP, o, b
}

// addressSpan is one contiguous run of addresses, inclusive of both ends.
type addressSpan struct{ lo, hi *big.Int }

// CoversAddressFamily reports whether nets, merged, span an ENTIRE address family.
//
// This is the rule for a trusted-proxy list, and it is deliberately about coverage rather
// than about spelling. "0.0.0.0/0" is only the most obvious way to trust everyone;
// ["0.0.0.0/1","128.0.0.0/1"] and ["0.0.0.0/1","128.0.0.0/2","192.0.0.0/2"] do the same
// thing with properly-masked, non-default-route entries, and no per-entry test reaches
// them. Trusting every address makes every peer a trusted proxy, which is what lets a
// caller connecting directly have their forwarding headers believed (ADR-080).
//
// Merging is exact and there is no threshold: a list covering all-but-one-address is NOT
// rejected. See the residual note in ADR-080 — any cut-off would be arbitrary and would
// refuse legitimate large lists, and a list built that way is not an accident.
func CoversAddressFamily(nets []*net.IPNet, bits int) bool {
	var spans []addressSpan
	for _, n := range nets {
		if n == nil || n.IP == nil {
			continue
		}
		ip, ones, netBits := NormalizeIPNet(n)
		if netBits != bits {
			continue
		}
		lo := new(big.Int).SetBytes(ip.Mask(net.CIDRMask(ones, netBits)))
		size := new(big.Int).Lsh(big.NewInt(1), uint(netBits-ones))
		hi := new(big.Int).Sub(new(big.Int).Add(lo, size), big.NewInt(1))
		spans = append(spans, addressSpan{lo, hi})
	}
	if len(spans) == 0 {
		return false
	}
	slices.SortFunc(spans, func(a, b addressSpan) int { return a.lo.Cmp(b.lo) })

	// The family is covered only if the merged run starts at zero and reaches the top
	// with no gap. Any gap is an address the list does not trust, which is the whole
	// point of having a list.
	if spans[0].lo.Sign() != 0 {
		return false
	}
	one := big.NewInt(1)
	reach := spans[0].hi
	for _, s := range spans[1:] {
		if s.lo.Cmp(new(big.Int).Add(reach, one)) > 0 {
			return false
		}
		// reach is the running MAXIMUM, not the last endpoint seen: CIDR sets nest freely
		// (10.0.0.0/8 sits inside 0.0.0.0/1), and a nested span would otherwise pull reach
		// backwards and invent a gap the list does not have.
		reach = slices.MaxFunc([]*big.Int{reach, s.hi}, (*big.Int).Cmp)
	}
	top := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(bits)), one)
	return reach.Cmp(top) == 0
}

// rejectTotalCoverage fails when a trusted-proxy list trusts an entire address family.
//
// Entries net.ParseCIDR rejects are skipped rather than reported: the two lenient keys
// tolerate a partial-invalid list on purpose, and validateCIDRList still owns that
// judgement. This rule adds one refusal; it does not tighten the syntax.
func rejectTotalCoverage(field string, list []string) error {
	var nets []*net.IPNet
	var parsed []string
	for _, entry := range list {
		trimmed := strings.TrimSpace(entry)
		if _, n, err := net.ParseCIDR(trimmed); err == nil {
			nets = append(nets, n)
			parsed = append(parsed, trimmed)
		}
	}
	for _, bits := range []int{net.IPv4len * 8, net.IPv6len * 8} {
		if !CoversAddressFamily(nets, bits) {
			continue
		}
		// One entry doing it alone keeps the message this repo has always used; a set
		// doing it together names the set, because no single entry is at fault.
		if len(parsed) == 1 {
			return newDefaultRouteError(field, parsed[0])
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    field,
			Message: fmt.Sprintf("entries %v together trust every address, which restores X-Forwarded-For spoofing",
				parsed),
			Action: actionListSpecificProxyRanges,
		}
	}
	return nil
}

// validateIPOrCIDRList rejects an entry that is neither a bare IP address nor a CIDR
// range. It is the allowlist counterpart to validateCIDRList: bare addresses are accepted
// because the shipped debug.allowedips default is ["127.0.0.1","::1"], which the strict
// proxy parser refuses.
//
// A default route is NOT an error here. An allowlist that admits everything is a
// legitimate posture — ADR-049 recommends ["0.0.0.0/0"] for exactly that — whereas a trust
// list that trusts everything re-opens header spoofing. The two keys look alike and mean
// opposite things.
//
// Host bits ARE an error, for the same reason ParseTrustedProxyCIDR refuses them on the
// proxy keys: "192.168.1.55/16" silently admits 65,536 hosts where the operator wrote one
// address, and nobody writes that intending a /16.
//
// Quotes are stripped exactly as app.IPWhitelist.cleanIPString strips them at runtime: a
// startup check stricter than the runtime parser would abort a deployment the runtime
// would have served (DEBUG_ALLOWEDIPS='"127.0.0.1"' works today).
//
// An entry that trims to empty is SKIPPED, not rejected — ADR-078's check inspects the raw
// koanf value and never sees one empty item inside a populated list, and NewIPWhitelist
// discards it silently. Deliberate, not a handoff.
func validateIPOrCIDRList(field string, list []string) error {
	for _, entry := range list {
		trimmed := strings.Trim(strings.TrimSpace(entry), "\"'")
		if trimmed == "" {
			continue
		}
		if net.ParseIP(trimmed) != nil {
			continue
		}
		ip, ipNet, err := net.ParseCIDR(trimmed)
		if err != nil {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    field,
				Message:  fmt.Sprintf("'%s' is neither an IP address nor a CIDR range", trimmed),
				Action:   "use an IP address (127.0.0.1) or a CIDR range (10.0.0.0/8); comma-separate multiple values in one env var",
			}
		}
		if !ip.Equal(ipNet.IP) {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    field,
				Message:  fmt.Sprintf("'%s' has host bits set, which silently widens the allowed range to %s", trimmed, ipNet),
				Action:   "write the network address (" + ipNet.String() + ") or the single host without a prefix",
			}
		}
	}
	return nil
}

// validateCIDRList fails when a non-empty list contains zero parseable CIDRs.
// Empty lists are valid (localhost-only / no-trusted-proxy defaults). Partial-invalid
// lists pass here and keep the existing middleware-time WARN so a single typo does not
// crash startup, while an all-invalid security control fails fast instead of silently
// degrading to a more restrictive (or, for redaction, weaker) posture.
//
// The parse loop intentionally mirrors scheduler/cidr_middleware.go's parser; config
// cannot import scheduler (import cycle), so the few lines are duplicated rather than shared.
func validateCIDRList(field string, list []string) error {
	if len(list) == 0 {
		return nil
	}
	var invalid []string
	valid := 0
	for _, entry := range list {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(entry)); err != nil {
			invalid = append(invalid, entry)
			continue
		}
		valid++
	}
	if valid == 0 {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    field,
			Message:  fmt.Sprintf("no valid CIDR entries (all %d rejected: %v)", len(list), invalid),
			Action:   "use CIDR notation, e.g. 10.0.0.0/8; comma-separate multiple values in one env var",
		}
	}
	return nil
}
