// Package sealed wires the jose/sealed codec into the messaging typed doors. Importing it
// (a blank import is enough) registers the codec from init; without the import a
// seal-tagged event type fails startup with messaging.ErrSealingNotLinked (ADR-097, on the
// ADR-091 opt-in-at-the-build-graph pattern).
package sealed

import "github.com/gaborage/go-bricks/messaging"

//nolint:gochecknoinits // link-time registration is the whole point of this package (ADR-091 pattern)
func init() {
	messaging.RegisterSealCodec(codec{})
}
