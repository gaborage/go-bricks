package keystore

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/gaborage/go-bricks/jose"
)

// maxLogicalKidLen caps a Logical kid so the full entry name stays a
// tractable header value once a generation suffix is appended (spec G4).
const maxLogicalKidLen = 64

// generationSuffixPattern detects the Generation marker: a trailing "-v"
// followed by digits only. Any entry name matching it IS a generation entry
// and is judged by the family rules below; an entry without it is an ordinary
// entry and is left alone.
var generationSuffixPattern = regexp.MustCompile(`-v\d+$`)

// generationVersionPattern is the canonical form of a version: a positive
// integer with no leading zero, so "v1" and "v01" can never name the same key
// and "v0" is not a generation. config.sealGenerationPattern holds the
// Activation selector value (messaging.seal.active) to the same grammar —
// keep in sync.
var generationVersionPattern = regexp.MustCompile(`^v[1-9]\d*$`)

// Role is the material an entry holds, which decides what a sealing side can
// do with a generation: verify/encrypt (public only), sign/decrypt (private
// present), or MAC (symmetric secret).
type Role uint8

const (
	// RolePublicOnly holds an RSA public key and no private key.
	RolePublicOnly Role = iota + 1
	// RolePrivate holds an RSA pair, private included.
	RolePrivate
	// RoleSecret holds raw symmetric material.
	RoleSecret
)

// String returns the role name used in errors and logs.
func (r Role) String() string {
	switch r {
	case RolePublicOnly:
		return "public-only"
	case RolePrivate:
		return "private"
	case RoleSecret:
		return "secret"
	default:
		return fmt.Sprintf("Role(%d)", uint8(r))
	}
}

// Generation is one provisioned key of a Logical kid's family: the entry
// named <Logical>-<Version> and the role its material grants.
type Generation struct {
	// Logical is the family name the sealing declaration carries.
	Logical string
	// Version is the generation marker without the hyphen, e.g. "v2".
	Version string
	// Role is what the entry's material permits.
	Role Role
}

// Kid is the full entry name, e.g. "svc-payments-sign-v2" — the value that
// travels on the wire and the name the store's accessors take.
func (g Generation) Kid() string {
	return g.Logical + "-" + g.Version
}

// FamilyEnumerator lists the provisioned generations of a Logical kid. The
// keystore's store implements it; consumers type-assert app.KeyStore to reach
// it, so the app.KeyStore interface itself is unchanged. The result IS the
// accept set: provisioning key material is the sole trust act (#1306).
type FamilyEnumerator interface {
	// Generations returns the provisioned generations of logical, ascending by
	// version, or an empty slice when none is provisioned. Never an error: an
	// unknown family is simply empty.
	Generations(logical string) []Generation
}

var _ FamilyEnumerator = (*store)(nil)

// Generations implements FamilyEnumerator. The returned slice is the caller's.
func (s *store) Generations(logical string) []Generation {
	return slices.Clone(s.families[logical])
}

// splitGeneration returns the family part and version of a generation entry
// name, or ok=false when the name carries no generation marker and is an
// ordinary entry. The LAST "-v<digits>" is the marker, so "x-v1-v2" splits
// into family "x-v1" and version "v2" — and the family then fails
// validateLogical, which is the intended refusal.
func splitGeneration(name string) (logical, version string, ok bool) {
	loc := generationSuffixPattern.FindStringIndex(name)
	if loc == nil {
		return "", "", false
	}
	return name[:loc[0]], name[loc[0]+1:], true
}

// validateLogical enforces the Logical kid grammar (spec G4): the jose kid
// alphabet, at most maxLogicalKidLen characters, and never itself ending in
// the generation marker, so every entry belongs to exactly one family.
func validateLogical(logical string) error {
	if !jose.ValidKid(logical) {
		return fmt.Errorf("logical kid %q is not a valid jose kid (allowed: A-Z a-z 0-9 _ -)", logical)
	}
	if len(logical) > maxLogicalKidLen {
		return fmt.Errorf("logical kid %q is %d characters, maximum is %d", logical, len(logical), maxLogicalKidLen)
	}
	if generationSuffixPattern.MatchString(logical) {
		return fmt.Errorf("logical kid %q must not end in the generation marker -v<digits>", logical)
	}
	return nil
}

// familyOf classifies one loaded entry. Ordinary entries return ok=false and
// no error; a generation entry whose family or version fails the grammar is
// refused, which newStore turns into a startup failure.
func familyOf(name string, entry *keyEntry) (Generation, bool, error) {
	logical, version, ok := splitGeneration(name)
	if !ok {
		return Generation{}, false, nil
	}
	if err := validateLogical(logical); err != nil {
		return Generation{}, false, fmt.Errorf("keystore: key %q: %w", name, err)
	}
	if !generationVersionPattern.MatchString(version) {
		return Generation{}, false, fmt.Errorf("keystore: key %q: generation %q must be a positive integer without leading zeros (v1, not v0 or v01)", name, version)
	}
	return Generation{Logical: logical, Version: version, Role: roleOf(entry)}, true, nil
}

func roleOf(entry *keyEntry) Role {
	switch {
	case entry.secret != nil:
		return RoleSecret
	case entry.private != nil:
		return RolePrivate
	default:
		return RolePublicOnly
	}
}

// indexFamilies groups the loaded entries by Logical kid, each family sorted
// ascending by version. names is the sorted entry order newStore already
// built, so the first refusal names the same key every run.
func indexFamilies(names []string, entries map[string]*keyEntry) (map[string][]Generation, error) {
	families := make(map[string][]Generation)
	for _, name := range names {
		gen, ok, err := familyOf(name, entries[name])
		if err != nil {
			return nil, err
		}
		if ok {
			families[gen.Logical] = append(families[gen.Logical], gen)
		}
	}
	for _, gens := range families {
		slices.SortFunc(gens, CompareGenerations)
	}
	return families, nil
}

// CompareGenerations orders two generations of one family by version, the
// order FamilyEnumerator guarantees. Versions are canonical decimal, so a
// shorter digit string is the smaller integer and equal lengths compare
// lexically — no integer parse, no overflow ceiling on the digit count.
func CompareGenerations(a, b Generation) int {
	if c := len(a.Version) - len(b.Version); c != 0 {
		return c
	}
	return strings.Compare(a.Version, b.Version)
}
