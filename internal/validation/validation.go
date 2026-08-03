// Package validation constructs the framework's shared go-playground validator
// instance. It is the framework's only construction site, so server and
// messaging start from the same custom-rule set. It does not freeze that set:
// server.Validator() hands out the live instance, so anything registered through
// it afterwards applies to HTTP alone.
//
// SECURITY: as of validator v10.30.3 all four name accessors on a FieldError —
// Namespace, StructNamespace, Field and StructField — derive from one string
// built with map keys interpolated verbatim, so a namespace from a dived map
// embeds payload data. Redact the bracketed span before logging one; the
// messaging package's PayloadError.Fields does exactly that.
package validation

import (
	"regexp"

	"github.com/go-playground/validator/v10"
)

// mccCodeRegex is pre-compiled for efficiency and to avoid error handling on each validation call.
// MCC codes are exactly 4 digits (e.g., "5411" for grocery stores).
var mccCodeRegex = regexp.MustCompile(`^\d{4}$`)

// New returns a validator with the framework's custom rules registered.
// The panic is unreachable today — RegisterValidation errors only on an empty
// tag name or a nil function, and every registration below is a static pair of
// both. It stays as a fail-fast in case that contract changes, since returning
// a half-configured instance would silently skip the rule at every call site.
func New() *validator.Validate {
	v := validator.New()
	// Every tag registered below also needs a case in server.getErrorMessage;
	// without one, HTTP callers get the generic "failed validation" message.
	if err := v.RegisterValidation("mcc_code", validateMCCCode); err != nil {
		panic("validation: registering mcc_code: " + err.Error())
	}

	return v
}

// Custom validator for MCC codes. The anchored regex is the whole rule: a
// separate length check would make each redundant with the other, so neither
// could be pinned by a test.
func validateMCCCode(fl validator.FieldLevel) bool {
	return mccCodeRegex.MatchString(fl.Field().String())
}
