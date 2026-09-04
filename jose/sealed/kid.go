package sealed

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/gaborage/go-bricks/jose"
)

// MaxLogicalKidLen caps a Logical kid; the concrete Generation name adds `-v<N>` on top.
const MaxLogicalKidLen = 64

var (
	// anyGenerationSuffix is the marker a Logical kid may never end in (G4): any `-v<digits>`,
	// leading zeros included, so `x-v01` is refused as a family name even though it is not
	// a well-formed Generation either.
	anyGenerationSuffix = regexp.MustCompile(`-v\d+$`)
	// generationKid splits a concrete kid into family and Generation. Generations are
	// positive integers without leading zeros (#1309 resolution 9).
	generationKid = regexp.MustCompile(`^(.+)-v([1-9]\d*)$`)
)

// CheckLogicalKid reports why s is not a Logical kid, or nil when it is one: the jose kid
// grammar `^[A-Za-z0-9_-]+$`, at most MaxLogicalKidLen characters, and not ending in the
// Generation marker `-v<digits>`.
func CheckLogicalKid(s string) error {
	if !jose.ValidKid(s) {
		return errors.New("must match ^[A-Za-z0-9_-]+$")
	}
	if len(s) > MaxLogicalKidLen {
		return fmt.Errorf("exceeds %d characters", MaxLogicalKidLen)
	}
	if anyGenerationSuffix.MatchString(s) {
		return errors.New("must not end in -v<digits> (that is a generation name)")
	}
	return nil
}

// SplitGenerationKid parses a concrete kid `<logical>-v<N>` into its family and Generation.
// ok is false when the suffix is not `-v[1-9][0-9]*` or the family part is not itself a
// Logical kid — so `x-v1-v2` is no Generation of anything, and `x-v0`/`x-v01` are refused.
func SplitGenerationKid(kid string) (family string, generation int, ok bool) {
	m := generationKid.FindStringSubmatch(kid)
	if m == nil {
		return "", 0, false
	}
	if CheckLogicalKid(m[1]) != nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}
