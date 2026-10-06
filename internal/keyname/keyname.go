// Package keyname is the one home of the key-name grammar: the user-chosen
// config section key, the keystore entry name (a dotted path of segments,
// ADR-144), the jose kid, the Logical kid a sealing family carries, and the
// Generation marker that names one key of a family. config, keystore, jose and
// jose/sealed judge names through it, so the rules cannot drift between copies.
// It reports verdicts, never messages: each caller keeps its own error text.
// Stdlib only, so config, the lowest layer, can import it.
package keyname

import (
	"regexp"
	"slices"
	"strings"
)

// MaxLogicalLen caps a Logical kid in bytes, so a concrete Generation name
// stays a tractable header value once its marker is appended (spec G4).
const MaxLogicalLen = 64

// Sep joins the segments of a dotted name. It is the config path delimiter, so
// a nested YAML path and the environment variable that reaches it name the
// same entry.
const Sep = "."

// FieldSegments are the field names of a keystore entry (config.KeyPairConfig).
// The keystore.keys tree reader recognizes an entry by them, so none may follow
// a '.' in an entry name: "webhook.secret" would read as entry "webhook" with a
// secret field. A first segment is never read as a field, so a legacy entry
// named "secret" stays valid. A config test pins this set to the struct's tags.
var FieldSegments = []string{"public", "private", "secret", "pkcs12"}

var (
	// sectionNamePattern is the grammar of a user-chosen config section key
	// under databases and multitenant.tenants (config.checkSectionName states
	// why, ADR-090), and of one segment of a dotted keystore entry name or seal
	// selector key (ADR-144).
	sectionNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	// kidPattern restricts key identifiers to ASCII alphanumerics, underscore and
	// hyphen, so no character can be misread by header processing or log sinks.
	kidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// markerPattern detects the Generation marker: a trailing "-v" followed by
	// digits only, leading zeros included, so "x-v01" carries a marker even
	// though it is no well-formed Generation.
	markerPattern = regexp.MustCompile(`-v\d+$`)
	// versionPattern is the canonical version: a positive integer without a
	// leading zero, so "v1" and "v01" never name the same key and "v0" is none.
	versionPattern = regexp.MustCompile(`^v[1-9]\d*$`)
)

// ValidSectionName reports whether name is a user-chosen config section key:
// one or more lowercase ASCII letters, digits or hyphens.
func ValidSectionName(name string) bool {
	return sectionNamePattern.MatchString(name)
}

// ValidName reports whether name is a keystore entry name or a seal selector
// key: one or more segments of lowercase ASCII letters, digits or hyphens,
// joined by '.', with no empty segment. Every valid section name is a
// one-segment name. ReservedAfterDot is the entry-only rule on top.
func ValidName(name string) bool {
	for seg := range strings.SplitSeq(name, Sep) {
		if !sectionNamePattern.MatchString(seg) {
			return false
		}
	}
	return true
}

// ReservedAfterDot reports the first FieldSegments word that follows a '.' in
// name, or bad=false when there is none. Only a keystore entry name is held to
// it: a selector key carries no fields.
func ReservedAfterDot(name string) (word string, bad bool) {
	_, rest, dotted := strings.Cut(name, Sep)
	if !dotted {
		return "", false
	}
	for seg := range strings.SplitSeq(rest, Sep) {
		if slices.Contains(FieldSegments, seg) {
			return seg, true
		}
	}
	return "", false
}

// ValidEntryName reports whether name is a keystore entry name: ValidName,
// and no FieldSegments word after a '.'.
func ValidEntryName(name string) bool {
	_, reserved := ReservedAfterDot(name)
	return ValidName(name) && !reserved
}

// Fold replaces every '-' with '.'. Two names with equal folds differ only in
// '-' versus '.', which an operator reading an environment variable cannot
// tell apart. Fold only refuses look-alikes; it never resolves a name.
func Fold(name string) string {
	return strings.ReplaceAll(name, "-", Sep)
}

// IsDottedPrefix reports whether prefix names a node above name in the dotted
// path: "tokens" is a dotted prefix of "tokens.our", "token" is not.
func IsDottedPrefix(prefix, name string) bool {
	return strings.HasPrefix(name, prefix+Sep)
}

// FirstFoldClash reports the first name, in the order given, whose Fold equals
// an earlier one's, with that earlier name. Pass names sorted, so the pair is
// the same on every run.
func FirstFoldClash(names []string) (earlier, later string, found bool) {
	seen := make(map[string]string, len(names))
	for _, name := range names {
		if first, clash := seen[Fold(name)]; clash {
			return first, name, true
		}
		seen[Fold(name)] = name
	}
	return "", "", false
}

// FirstDottedPrefix reports the first pair, in the order given, in which one
// name is a dotted prefix of the other. Pass names sorted, so the pair is the
// same on every run.
func FirstDottedPrefix(names []string) (prefix, name string, found bool) {
	return firstNesting(names, IsDottedPrefix)
}

// FirstFoldedPrefix reports the first pair, in the order given, in which one
// name's Fold is a dotted prefix of the other's and either name contains '.':
// "payments-sign" and "payments.sign.eu". A POSIX variable reads '-' as '.',
// so the variable meant for the first lands on the path that holds the
// second. Two names without '.' are exempt: neither has a nested path for a
// variable to land on, and such a pair (payments-sign, payments-sign-eu)
// predates dotted names. Every dotted prefix is a folded one, so this widens
// FirstDottedPrefix. Pass names sorted, so the pair is the same on every run.
func FirstFoldedPrefix(names []string) (prefix, name string, found bool) {
	return firstNesting(names, isFoldedPrefix)
}

func isFoldedPrefix(prefix, name string) bool {
	return (strings.Contains(prefix, Sep) || strings.Contains(name, Sep)) && IsDottedPrefix(Fold(prefix), Fold(name))
}

// firstNesting reports the first pair, in the order given, that nests
// reports true for, with the outer name first.
func firstNesting(names []string, nests func(prefix, name string) bool) (prefix, name string, found bool) {
	for i, a := range names {
		for _, b := range names[i+1:] {
			switch {
			case nests(a, b):
				return a, b, true
			case nests(b, a):
				return b, a, true
			}
		}
	}
	return "", "", false
}

// ValidKid reports whether kid is a well-formed key identifier: one or more
// ASCII alphanumerics, underscores or hyphens.
func ValidKid(kid string) bool {
	return kidPattern.MatchString(kid)
}

// ValidVersion reports whether version is a canonical Generation version, the
// marker without its hyphen: "v" and a positive integer with no leading zero.
func ValidVersion(version string) bool {
	return versionPattern.MatchString(version)
}

// LogicalFault is the first rule a candidate Logical kid breaks.
type LogicalFault uint8

const (
	// LogicalOK means the name is a Logical kid.
	LogicalOK LogicalFault = iota
	// LogicalNotKid means the name fails ValidKid, the empty name included.
	LogicalNotKid
	// LogicalTooLong means the name is longer than MaxLogicalLen bytes.
	LogicalTooLong
	// LogicalEndsInMarker means the name ends in the Generation marker
	// -v<digits>, so it would read as a Generation of another family.
	LogicalEndsInMarker
)

// CheckLogical reports the first rule logical breaks, in a fixed order: the
// jose kid alphabet, then the length cap, then the marker. A Logical kid never
// ends in the marker, so no entry name can belong to two families.
func CheckLogical(logical string) LogicalFault {
	switch {
	case !ValidKid(logical):
		return LogicalNotKid
	case len(logical) > MaxLogicalLen:
		return LogicalTooLong
	case markerPattern.MatchString(logical):
		return LogicalEndsInMarker
	default:
		return LogicalOK
	}
}

// Form classifies a name by its Generation marker.
type Form uint8

const (
	// Ordinary is a name with no Generation marker.
	Ordinary Form = iota
	// Generation is a marked name whose family is a Logical kid and whose
	// version is canonical.
	Generation
	// Malformed is a marked name whose family or version breaks the grammar.
	Malformed
)

// SplitGeneration splits name at its Generation marker into the family and the
// version, which keeps its "v". The LAST "-v<digits>" is the marker, so
// "x-v1-v2" splits into family "x-v1" and version "v2", and is Malformed
// because that family ends in a marker itself. An Ordinary name returns two
// empty strings; a Malformed one returns both parts, for the caller to name.
func SplitGeneration(name string) (logical, version string, form Form) {
	loc := markerPattern.FindStringIndex(name)
	if loc == nil {
		return "", "", Ordinary
	}
	logical, version = name[:loc[0]], name[loc[0]+1:]
	if CheckLogical(logical) != LogicalOK || !ValidVersion(version) {
		return logical, version, Malformed
	}
	return logical, version, Generation
}

// GenerationName composes the concrete name of one Generation of a family,
// e.g. "svc-payments-sign" and "v2" make "svc-payments-sign-v2": the keystore
// entry name and the kid that travels on the wire.
func GenerationName(logical, version string) string {
	return logical + "-" + version
}
