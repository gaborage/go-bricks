// Package identifier validates a single bare, unquoted SQL identifier segment
// against its vendor's grammar and byte cap. It is a leaf: it imports only the
// standard library and database/types, so a consumer that must validate a
// schema or role name before opening a connection can import it alone.
package identifier

import (
	"errors"
	"fmt"
	"regexp"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// Byte caps per vendor. Length is measured in bytes, not runes: that is the
// unit the server truncates on.
const (
	// MaxPostgreSQLBytes is NAMEDATALEN-1. PostgreSQL silently truncates
	// longer names, so two 64-byte names sharing a prefix collapse onto one
	// object; refusing them here surfaces the collision at the boundary.
	MaxPostgreSQLBytes = 63
	// MaxOracleBytes is the Oracle 12.2+ limit (earlier releases cap at 30
	// and are not modeled). Oracle raises ORA-00972 rather than truncating.
	MaxOracleBytes = 128
)

// Sentinels, one per refusal class. Each is wrapped with the offending value
// (or vendor) so errors.Is works and the message still names the input.
var (
	ErrEmptyIdentifier   = errors.New("identifier: empty")
	ErrIdentifierCharset = errors.New("identifier: character outside the vendor grammar")
	ErrIdentifierTooLong = errors.New("identifier: exceeds the vendor byte cap")
	ErrUnsupportedVendor = errors.New("identifier: unsupported vendor")
)

type grammar struct {
	segment  *regexp.Regexp
	maxBytes int
}

// The patterns are a deliberate conservative ASCII subset of each vendor's
// unquoted-identifier grammar, not the vendor's full character-set rule:
// non-ASCII letters the server would accept are rejected by policy.
var grammars = map[dbtypes.Vendor]grammar{
	dbtypes.PostgreSQL: {segment: regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`), maxBytes: MaxPostgreSQLBytes},
	dbtypes.Oracle:     {segment: regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]*$`), maxBytes: MaxOracleBytes},
}

// Validate reports whether value is one bare, unquoted identifier segment
// under vendor's grammar. Bare means no dots, alias or wildcard: validate a
// qualified name one segment at a time.
//
//   - dbtypes.PostgreSQL: ^[A-Za-z_][A-Za-z0-9_$]*$, at most MaxPostgreSQLBytes.
//   - dbtypes.Oracle: ^[A-Za-z_][A-Za-z0-9_$#]*$, at most MaxOracleBytes.
//   - any other vendor: ErrUnsupportedVendor.
//
// Both grammars are a deliberate conservative ASCII subset of the vendor's
// unquoted-identifier rule: a non-ASCII letter the server itself would accept
// is rejected here by policy, so accepted names are safe to splice unquoted
// on every path. The value is validated as given — never trimmed — so
// surrounding whitespace is rejected. Length is byte length and is checked before the grammar, so an
// over-long value reports the cap even when it also has a bad character. The
// cap exists because PostgreSQL silently truncates a longer name, so two
// over-long names sharing a prefix would collapse onto one object.
// Mixed case is accepted; note that the server folds an unquoted identifier —
// PostgreSQL to lowercase, Oracle to uppercase — so "Foo" and "foo" name the
// same object once unquoted.
//
// This is the identifier grammar. The query builder's lexer
// (database/internal/sqllex) tokenises a broader vendor-mixed character set
// and is not a substitute for it.
func Validate(vendor dbtypes.Vendor, value string) error {
	g, ok := grammars[vendor]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedVendor, vendor)
	}
	if value == "" {
		return ErrEmptyIdentifier
	}
	if len(value) > g.maxBytes {
		return fmt.Errorf("%w (%d): %q", ErrIdentifierTooLong, g.maxBytes, value)
	}
	if !g.segment.MatchString(value) {
		return fmt.Errorf("%w: %q", ErrIdentifierCharset, value)
	}
	return nil
}
