package provisioning

import (
	"context"
	"errors"

	"github.com/gaborage/go-bricks/migration"
)

// ErrQuiesced is returned by Run when the deployment quiesce flag is set and a
// job is therefore parked. It is NOT a failure: the job stays at its current
// persisted state (no step ran, no transition persisted) and a dispatcher
// should treat errors.Is(err, ErrQuiesced) as "retry later", never as a reason
// to move the job toward cleanup/failed.
var ErrQuiesced = errors.New("provisioning: paused by deployment quiesce flag")

// WithQuiesce enables the deployment quiesce gate for this Executor. When the
// supplied gate reports the flag set, Run parks the job (returns ErrQuiesced)
// instead of advancing it. Opt-in: an Executor without WithQuiesce (nil gate)
// never quiesces. Returns the Executor for chaining.
func (e *Executor) WithQuiesce(g migration.QuiesceGate) *Executor {
	e.quiesce = g
	return e
}

// quiesced reports whether provisioning is currently paused. Fail-open: if the
// quiesce check itself errors (e.g. a transient control-plane DB hiccup), it
// logs a WARN and returns false so a control-plane blip cannot strand all
// provisioning (#380 decision). A nil gate is never quiesced.
func (e *Executor) quiesced(ctx context.Context) bool {
	if e.quiesce == nil {
		return false
	}
	set, err := e.quiesce.IsSet(ctx)
	if err != nil {
		e.Logger.Warn().Err(err).Msg("quiesce check failed; proceeding (fail-open)")
		return false
	}
	return set
}
