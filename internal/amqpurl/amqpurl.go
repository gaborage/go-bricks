// Package amqpurl decides whether an AMQP broker URL may be dialed. It lives apart
// from internal/credurl so the credential-URL helper stays free of the AMQP client.
package amqpurl

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Hint is the remedy every refusal carries: the usual cause is a reserved character
// left unencoded in the credentials.
const Hint = "percent-encode reserved characters in the username and password (url.UserPassword)"

// The refusals are fixed text. A broker URL carries credentials, and the vendor's
// parse errors quote the URL, so neither reaches a message: only a reason class does.
var (
	ErrNotAMQPURL        = errors.New("is not a valid AMQP URL")
	ErrUnsupportedScheme = errors.New("uses an unsupported scheme")
	ErrPortOutOfRange    = errors.New("carries a port out of range")
	ErrQueryOutOfRange   = errors.New("carries a query parameter out of range")
)

const (
	minPort = 1
	maxPort = 65535
)

// Check reports why raw may not be dialed, or nil. The verdict is amqp.ParseURI's own,
// plus a written port outside 1-65535, which ParseURI accepts; the returned error only
// names the class, so a rule the vendor adds later still refuses, under the last one.
func Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || strings.Contains(raw, " ") {
		return ErrNotAMQPURL
	}
	if u.Scheme != "amqp" && u.Scheme != "amqps" {
		return ErrUnsupportedScheme
	}
	if !portInRange(u.Port()) {
		return ErrPortOutOfRange
	}
	if _, err := amqp.ParseURI(raw); err != nil {
		return ErrQueryOutOfRange
	}
	return nil
}

// portInRange accepts an unwritten port (the scheme's default applies) and a written
// one within 1-65535, leading zeros included.
func portInRange(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= minPort && n <= maxPort
}
