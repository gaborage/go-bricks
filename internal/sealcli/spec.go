package sealcli

import (
	"fmt"

	"github.com/gaborage/go-bricks/internal/keyname"
	"github.com/gaborage/go-bricks/jose/sealed"
)

// DocumentSpec builds the raw-document Spec from the -sign-kid, -encrypt-kid and -subject
// flags. The wire carries each concrete Generation while the Spec names its Logical family,
// so the CLI takes the concrete kid and splits it rather than asking the operator for both.
func DocumentSpec(signKid, encryptKid, subject string) (*sealed.Spec, error) {
	signFamily, err := splitFamily("-sign-kid", signKid)
	if err != nil {
		return nil, err
	}
	encryptFamily, err := splitFamily("-encrypt-kid", encryptKid)
	if err != nil {
		return nil, err
	}
	return sealed.NewDocumentSpec(signFamily, encryptFamily, subject)
}

// splitFamily reports the Logical family of a concrete kid, naming the flag that carried it
// so the operator knows which of the two to fix. The family fixes the marker (ADR-144), so
// a kid is a Generation in one of two shapes; the two likeliest mistakes — a family passed
// where its generation belongs, and the marker the family does not take — get the kid to
// pass instead.
func splitFamily(flagName, kid string) (string, error) {
	family, _, ok := sealed.SplitGenerationKid(kid)
	if ok {
		return family, nil
	}
	if sealed.CheckLogicalKid(kid) == nil {
		return "", fmt.Errorf("%s %q is a family, not a generation: pass %s", flagName, kid, keyname.GenerationName(kid, "v<N>"))
	}
	logical, version, form := keyname.SplitGeneration(kid)
	if form == keyname.Malformed && keyname.CheckLogical(logical) == keyname.LogicalOK && keyname.ValidVersion(version) {
		return "", fmt.Errorf("%s %q is not a generation: family %q takes the marker of %s", flagName, kid, logical, keyname.GenerationName(logical, version))
	}
	return "", fmt.Errorf("%s %q is not a generation: expected <family>.v<N> (family with '.') or <family>-v<N> (family without '.'), with N a positive integer without leading zeros", flagName, kid)
}
