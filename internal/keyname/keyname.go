// Package keyname is the one home of the key-name grammar: the user-chosen
// config section key, the jose kid, the Logical kid a sealing family carries,
// and the Generation marker that names one key of a family. config, keystore,
// jose and jose/sealed judge names through it, so the rules cannot drift
// between copies. It reports verdicts, never messages: each caller keeps its
// own error text. Stdlib only, so config, the lowest layer, can import it.
package keyname

import "regexp"

// MaxLogicalLen caps a Logical kid in bytes, so a concrete Generation name
// stays a tractable header value once its marker is appended (spec G4).
const MaxLogicalLen = 64

var (
	// sectionNamePattern is the grammar of a user-chosen config section key:
	// entries under databases, multitenant.tenants, keystore.keys and
	// messaging.seal.active. config.checkSectionName states why (ADR-090).
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
