package server

import (
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/internal/saferender"
)

// unauditedBindSummary is the fail-closed rendering for a bind failure whose
// shape has not been audited for request content.
const unauditedBindSummary = "cause withheld (unaudited bind failure)"

// bindError pairs a bind failure's raw cause with a payload-free summary.
//
// SECURITY: the cause is request-derived text on every source — a JSON decoder
// quotes the offending byte and names the input map key, and strconv quotes the
// rejected query/path/header value — so it may reach a log line (where the
// framework's field filter and #1168 apply) but never a response body. Error()
// keeps rendering the cause so request-completion logging is unchanged; the
// response path reads bindSummary instead.
type bindError struct {
	// summary names the binding source and the destination field by its struct
	// tag, never the input. Empty means the shape was not audited, and
	// bindSummary substitutes the fail-closed phrase.
	summary string
	err     error
}

func (e *bindError) Error() string {
	if e == nil || e.err == nil {
		return unauditedBindSummary
	}

	return e.err.Error()
}

func (e *bindError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.err
}

// bindSummary renders a bind failure for a response body. An error that is not a
// *bindError — nothing produces one today, but a future binding source might —
// fails closed rather than rendering its own text.
func bindSummary(err error) string {
	var be *bindError
	if errors.As(err, &be) && be.summary != "" {
		return be.summary
	}

	return unauditedBindSummary
}

// newJSONBindError wraps a JSON body decode failure. fieldPathIsSchema comes from
// the request type, decided once per route, never inferred from the error; see
// saferender.JSONDecodeSummary.
func newJSONBindError(cause error, fieldPathIsSchema bool) error {
	return &bindError{
		summary: saferender.JSONDecodeSummary(cause, fieldPathIsSchema),
		err:     fmt.Errorf("failed to bind JSON body: %w", cause),
	}
}

// label names a binding source in a summary and in the wrapped cause.
func (s bindSource) label() string {
	switch s {
	case bindSourceParam:
		return "path param"
	case bindSourceQuery:
		return "query param"
	case bindSourceHeader:
		return "header"
	default:
		return "request field"
	}
}

// newFieldBindError wraps a param/query/header bind failure. name is the struct
// tag's value — author-written schema, not request input — so naming it is safe;
// the rejected value never enters the summary.
func newFieldBindError(source bindSource, name string, cause error) error {
	return &bindError{
		summary: fmt.Sprintf("failed to bind %s %q", source.label(), name),
		err:     fmt.Errorf("failed to set %s %s: %w", source.label(), name, cause),
	}
}
