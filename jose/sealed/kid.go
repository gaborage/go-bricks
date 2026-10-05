package sealed

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gaborage/go-bricks/internal/keyname"
)

// MaxLogicalKidLen caps a Logical kid; the concrete Generation name adds its marker
// (`.v<N>` or `-v<N>`) on top.
const MaxLogicalKidLen = keyname.MaxLogicalLen

// CheckLogicalKid reports why s is not a Logical kid, or nil when it is one: the jose kid
// grammar `^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`, at most MaxLogicalKidLen characters, and
// not ending in a Generation marker — a trailing `-v<digits>`, or a final `v<digits>`
// segment after a '.' (ADR-144) — leading zeros included, so `x-v01` is refused as a family
// name even though it is not a well-formed Generation either.
func CheckLogicalKid(s string) error {
	switch keyname.CheckLogical(s) {
	case keyname.LogicalNotKid:
		return errors.New(`must match ^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)
	case keyname.LogicalTooLong:
		return fmt.Errorf("exceeds %d characters", MaxLogicalKidLen)
	case keyname.LogicalEndsInMarker:
		return errors.New("must not end in -v<digits> or .v<digits> (that is a generation name)")
	default:
		return nil
	}
}

// SplitGenerationKid parses a concrete kid into its family and Generation. The family
// fixes the marker (ADR-144): a family containing '.' takes a final `.v<N>` segment
// (`payments.sign.v2`), a family without one `-v<N>` (`svc-payments-sign-v2`). ok is false
// when the marker is not `[1-9][0-9]*` after the `v`, when it is the marker the family does
// not take (`payments.sign-v1`, `audit.v1`), or when the family part is not itself a
// Logical kid — so `x-v1-v2` and `x.v1.v2` are no Generation of anything, and
// `x-v0`/`x-v01` are refused (#1309 resolution 9). ok is also false when N overflows an
// int: the keystore never parses N, so it admits a generation this function refuses.
func SplitGenerationKid(kid string) (family string, generation int, ok bool) {
	family, version, form := keyname.SplitGeneration(kid)
	if form != keyname.Generation {
		return "", 0, false
	}
	n, err := strconv.Atoi(version[1:])
	if err != nil {
		return "", 0, false
	}
	return family, n, true
}
