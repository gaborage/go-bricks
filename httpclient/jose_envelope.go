package httpclient

import "encoding/json"

// visaMLEBody is the Visa Message Level Encryption wire object: a single encData member
// carrying the compact JWE. Both directions marshal/unmarshal through it, so the member
// name is spelled once.
type visaMLEBody struct {
	EncData string `json:"encData"`
}

// visaMLEEnvelope implements BodyEnvelope for Visa Message Level Encryption. It holds no
// state, so the zero value is usable and every JOSETransport carrying it stays comparable.
type visaMLEEnvelope struct{}

// VisaMLEEnvelope returns the BodyEnvelope implementing Visa Message Level Encryption's
// JSON envelope: outbound bodies are {"encData":"<compact JWE>"} sent as application/json,
// and inbound bodies are recognized by shape rather than Content-Type — any JSON object
// carrying a non-empty string encData member is unwrapped, and everything else passes
// through untouched. Unknown sibling members are ignored.
func VisaMLEEnvelope() BodyEnvelope {
	return visaMLEEnvelope{}
}

func (visaMLEEnvelope) Wrap(compact string) (body []byte, contentType string, err error) {
	encoded, err := json.Marshal(visaMLEBody{EncData: compact})
	if err != nil {
		return nil, "", err
	}
	return encoded, mimeApplicationJSON, nil
}

func (visaMLEEnvelope) Unwrap(_ string, body []byte) (compact string, ok bool) {
	var envelope visaMLEBody
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
