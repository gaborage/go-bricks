package httpclient

import "encoding/json"

// visaMLEEncDataField is the single member of the Visa Message Level Encryption
// envelope that carries the compact JWE.
const visaMLEEncDataField = "encData"

// VisaMLEEnvelope returns the WrapBody/UnwrapBody pair implementing Visa Message Level
// Encryption's JSON envelope: outbound bodies are {"encData":"<compact JWE>"} sent as
// application/json, and inbound bodies are recognized by shape rather than Content-Type —
// any JSON object carrying a non-empty string encData member is unwrapped, and everything
// else passes through untouched. Unknown sibling members are ignored.
func VisaMLEEnvelope() (WrapBodyFunc, UnwrapBodyFunc) {
	return visaMLEWrap, visaMLEUnwrap
}

func visaMLEWrap(compact string) (body []byte, contentType string, err error) {
	encoded, err := json.Marshal(map[string]string{visaMLEEncDataField: compact})
	if err != nil {
		return nil, "", err
	}
	return encoded, "application/json", nil
}

func visaMLEUnwrap(_ string, body []byte) (compact string, ok bool) {
	var envelope struct {
		EncData *string `json:"encData"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", false
	}
	if envelope.EncData == nil || *envelope.EncData == "" {
		return "", false
	}
	return *envelope.EncData, true
}
