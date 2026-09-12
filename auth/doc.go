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
// A Principal must likewise not be rendered field by field. Its String method
// elides the subject and every claim value, and covers the fmt verbs that
// consult fmt.Stringer (%v, %s, %q, %+v); %#v and struct-walking encoders such
// as json.Marshal bypass it and would print the subject.
package auth
