// Package types holds shared value types used across the API (a sibling package).
package types

// Money is defined in a sibling in-module package and referenced cross-package.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}
