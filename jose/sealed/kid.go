package sealed

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gaborage/go-bricks/internal/keyname"
)

// MaxLogicalKidLen caps a Logical kid; the concrete Generation name adds `-v<N>` on top.
const MaxLogicalKidLen = keyname.MaxLogicalLen

// CheckLogicalKid reports why s is not a Logical kid, or nil when it is one: the jose kid
// grammar `^[A-Za-z0-9_-]+$`, at most MaxLogicalKidLen characters, and not ending in the
// Generation marker `-v<digits>` — leading zeros included, so `x-v01` is refused as a
// family name even though it is not a well-formed Generation either.
func CheckLogicalKid(s string) error {
	switch keyname.CheckLogical(s) {
	case keyname.LogicalNotKid:
		return errors.New("must match ^[A-Za-z0-9_-]+$")
	case keyname.LogicalTooLong:
		return fmt.Errorf("exceeds %d characters", MaxLogicalKidLen)
	case keyname.LogicalEndsInMarker:
		return errors.New("must not end in -v<digits> (that is a generation name)")
	default:
		return nil
	}
}

// SplitGenerationKid parses a concrete kid `<logical>-v<N>` into its family and Generation.
// ok is false when the suffix is not `-v[1-9][0-9]*` or the family part is not itself a
// Logical kid — so `x-v1-v2` is no Generation of anything, and `x-v0`/`x-v01` are refused
// (#1309 resolution 9). ok is also false when N overflows an int: the keystore never
// parses N, so it admits a generation this function refuses.
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
