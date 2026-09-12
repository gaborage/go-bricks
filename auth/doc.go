// Package auth verifies bearer credentials (RFC 9068 style JWTs) and turns a
// verified credential into a Principal carried on the request context.
//
// The package performs identification, not authorization. A verified Principal
// states who the caller claims to be according to a trusted issuer; it grants
// nothing. Handlers remain responsible for deciding whether that identity may
// perform the requested operation — the middleware never short-circuits a
// request on authorization grounds, only on a missing or invalid credential.
//
// Verification is RSA-only: RS256 and PS256 are the sole accepted signature
// algorithms, and a key set entry that is not an RSA public key is not usable.
// Symmetric algorithms are deliberately unsupported, so a key-confusion
// downgrade to HS256 has no code path to reach.
//
// Nothing in this package logs, records, or renders the credential itself or
// the "sub" claim. Verification failures are reported by class (see
// VerificationError), which is safe to log at DEBUG; the credential string,
// signature bytes and subject never appear in an error message.
//
// VerificationError.Cause is diagnostic detail for framework DEBUG logging. Its
// contents come from whichever library rejected the credential, so they are not
// part of this package's compatibility promise and may change without notice.
// Callers must not render a Cause into a response body: log the Class instead,
// which is the only failure detail the package guarantees is safe to expose.
//
// A Principal must likewise not be rendered field by field, and it closes that
// hole itself: String, Format and MarshalJSON all derive from one redacted
// shape that keeps the issuer, audience and expiry and replaces the subject and
// every claim value with an elision marker. Implementing fmt.Formatter takes
// precedence over fmt.Stringer for every verb, so %v, %s, %q, %+v and %#v are
// all elided — for both Principal and *Principal — and MarshalJSON covers
// json.Marshal and the encoders built on it.
//
// One path bypasses all of that: the framework logger's reflective filter
// (logger.LogEventAdapter.Interface → SensitiveDataFilter.FilterValue →
// filterStructWithProtection) rebuilds a struct into a map[string]any by
// reflection, reading exported fields directly before any marshaler or
// formatter runs. No method on Principal can influence it, and the filter
// matches field NAMES, so neither "Subject" nor an issuer-chosen claim key is
// masked. Do not hand a Principal to logger.Interface or to WithFields.
package auth
