package httpclient

import "encoding/json"

// visaMLEEnvelope is the Visa Message Level Encryption wire object: a single encData
// member carrying the compact JWE. Both directions marshal/unmarshal through it, so the
// member name is spelled once.
type visaMLEEnvelope struct {
	EncData string `json:"encData"`
}

// VisaMLEEnvelope returns the WrapBody/UnwrapBody pair implementing Visa Message Level
// Encryption's JSON envelope: outbound bodies are {"encData":"<compact JWE>"} sent as
// application/json, and inbound bodies are recognized by shape rather than Content-Type —
// any JSON object carrying a non-empty string encData member is unwrapped, and everything
// else passes through untouched. Unknown sibling members are ignored.
func VisaMLEEnvelope() (WrapBodyFunc, UnwrapBodyFunc) {
	return visaMLEWrap, visaMLEUnwrap
}

func visaMLEWrap(compact string) (body []byte, contentType string, err error) {
	encoded, err := json.Marshal(visaMLEEnvelope{EncData: compact})
	if err != nil {
		return nil, "", err
	}
	return encoded, mimeApplicationJSON, nil
}

func visaMLEUnwrap(_ string, body []byte) (compact string, ok bool) {
	var envelope visaMLEEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", false
	}
	// A missing member, a JSON null and an empty string all land on "" and are all
	// rejected; a non-string encData never gets this far, Unmarshal fails on it.
	if envelope.EncData == "" {
		return "", false
	}
	return envelope.EncData, true
}
