package keystore

import (
	"fmt"
	"strings"
)

// ActiveGeneration resolves the producer's Activation for one Logical kid:
// which provisioned generation seals new traffic. active is the
// messaging.seal.active selector (Logical kid -> "v<N>"), already
// shape-checked by config.Validate; store is the keystore's family index.
//
//   - no provisioned generation: error naming the family, selector or not;
//   - one provisioned, no selector: that one is active;
//   - several provisioned, no selector: error — startup never guesses;
//   - selector present: it must name a provisioned generation, else an error
//     naming the selector value.
//
// The caller (the component owning sealing declarations) calls it once per
// Logical kid it resolves, sign and encrypt alike, at startup.
func ActiveGeneration(store FamilyEnumerator, active map[string]string, logical string) (Generation, error) {
	if err := validateLogical(logical); err != nil {
		return Generation{}, fmt.Errorf("keystore: %w", err)
	}
	gens := store.Generations(logical)
	if len(gens) == 0 {
		return Generation{}, fmt.Errorf("keystore: logical kid %q has no provisioned generation (expected a keystore.keys entry named %s-v<N>)", logical, logical)
	}

	selector, selected := active[logical]
	if !selected {
		if len(gens) == 1 {
			return gens[0], nil
		}
		return Generation{}, fmt.Errorf("keystore: logical kid %q has %d provisioned generations (%s) and no messaging.seal.active.%s selector",
			logical, len(gens), versionList(gens), logical)
	}
	// config.Validate holds the selector to this grammar; repeated here for a
	// hand-built config that skipped it, so a malformed selector is named
	// rather than reported as merely unprovisioned.
	if !generationVersionPattern.MatchString(selector) {
		return Generation{}, fmt.Errorf("keystore: messaging.seal.active.%s = %q is not a generation (v1, not v0 or v01)", logical, selector)
	}
	for _, gen := range gens {
		if gen.Version == selector {
			return gen, nil
		}
	}
	return Generation{}, fmt.Errorf("keystore: messaging.seal.active.%s = %q names an unprovisioned generation (provisioned: %s)",
		logical, selector, versionList(gens))
}

func versionList(gens []Generation) string {
	versions := make([]string, len(gens))
	for i, gen := range gens {
		versions[i] = gen.Version
	}
	return strings.Join(versions, ", ")
}
