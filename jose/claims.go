package jose

import "time"

// Claims is the verified claim set extracted from a successfully decrypted-and-verified
// inbound JOSE payload. The middleware sets it on the request context so application
// handlers can enforce iat/exp/jti policies (per the v1 decision: framework verifies the
// signature, applications enforce timing).
//
// All fields are zero-valued if absent from the JWS payload; nothing here is required
// at the framework layer — apps that don't care about a particular claim simply ignore it.
type Claims struct {
	Issuer    string
	Subject   string
	Audience  []string
	IssuedAt  time.Time
	ExpiresAt time.Time
	NotBefore time.Time
	JTI       string
	// Raw is the full decoded claim map for fields not promoted above (vendor extensions,
	// custom claims, etc.). Apps cast values themselves.
	Raw map[string]any
}

// parseUnixSecs accepts the JSON-decoded value of an iat/exp/nbf claim (which JSON
// decodes as float64) and returns the corresponding time.Time. Returns the zero value
// if the input is missing or not a number.
func parseUnixSecs(v any) time.Time {
	switch n := v.(type) {
	case float64:
		return time.Unix(int64(n), 0).UTC()
	case int64:
		return time.Unix(n, 0).UTC()
	case int:
		return time.Unix(int64(n), 0).UTC()
	default:
		return time.Time{}
	}
}
