package sealcli

import (
	"fmt"

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
// so the operator knows which of the two to fix.
func splitFamily(flagName, kid string) (string, error) {
	family, _, ok := sealed.SplitGenerationKid(kid)
	if !ok {
		return "", fmt.Errorf("%s %q is not a generation: expected <logical>-v<N> with N a positive integer without leading zeros", flagName, kid)
	}
	return family, nil
}
