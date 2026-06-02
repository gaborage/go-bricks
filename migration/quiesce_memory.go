package migration

import (
	"context"
	"sync"
	"time"
)

// quiesceRecord is the persisted shape of a single scope's flag, shared by the
// memory and postgres controllers' in-Go status derivation.
type quiesceRecord struct {
	setAt     time.Time
	setBy     string
	reason    string
	expiresAt time.Time
	clearedAt *time.Time
}

// statusFrom derives the operator-facing QuiesceStatus from a stored record at
// time now. Active = uncleared and unexpired; Expired = uncleared but past TTL.
func statusFrom(rec *quiesceRecord, now time.Time) *QuiesceStatus {
	st := &QuiesceStatus{
		SetAt:     rec.setAt,
		SetBy:     rec.setBy,
		Reason:    rec.reason,
		ExpiresAt: rec.expiresAt,
		ClearedAt: rec.clearedAt,
	}
	if rec.clearedAt == nil {
		if now.Before(rec.expiresAt) {
			st.Active = true
		} else {
			st.Expired = true
		}
	}
	return st
}

// MemoryQuiesceController is an in-memory QuiesceController for unit tests and
// single-process use. The Set/Clear/IsSet/Query operations are safe for
// concurrent use (mu guards the rec). WithClock/WithAudit are builder methods —
// configure them before concurrent use, mirroring MemoryStore. Construct with
// NewMemoryQuiesceController.
type MemoryQuiesceController struct {
	mu    sync.Mutex // guards rec
	rec   *quiesceRecord
	now   func() time.Time // configured via WithClock before use
	audit Emitter          // configured via WithAudit before use
}

// NewMemoryQuiesceController returns an empty in-memory controller (not quiesced).
func NewMemoryQuiesceController() *MemoryQuiesceController {
	return &MemoryQuiesceController{}
}

// WithAudit enables quiesce.set / quiesce.cleared audit emission through the
// shared migration.Emitter. The audited principal is the operator who performed
// the action (QuiesceSetOptions.By / the Clear `by` argument), never inferred.
func (c *MemoryQuiesceController) WithAudit(em Emitter) *MemoryQuiesceController {
	c.audit = em
	return c
}

// WithClock installs a deterministic clock (tests). Passing nil restores
// time.Now. Builder method — call before concurrent use. Returns the controller
// for chaining.
func (c *MemoryQuiesceController) WithClock(now func() time.Time) *MemoryQuiesceController {
	c.now = now
	return c
}

func (c *MemoryQuiesceController) timestamp() time.Time {
	if c.now == nil {
		return time.Now().UTC()
	}
	return c.now()
}

// IsSet reports whether the flag is currently active (uncleared and unexpired).
func (c *MemoryQuiesceController) IsSet(_ context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rec == nil {
		return false, nil
	}
	return statusFrom(c.rec, c.timestamp()).Active, nil
}

// Query returns the full status snapshot.
func (c *MemoryQuiesceController) Query(_ context.Context) (*QuiesceStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rec == nil {
		return &QuiesceStatus{}, nil
	}
	return statusFrom(c.rec, c.timestamp()), nil
}

// Set activates (or renews) the flag.
func (c *MemoryQuiesceController) Set(ctx context.Context, opts QuiesceSetOptions) (*QuiesceStatus, error) {
	now := c.timestamp()
	ttl, err := resolveTTL(opts.TTL)
	if err != nil {
		return nil, err
	}
	rec := &quiesceRecord{
		setAt:     now,
		setBy:     opts.By,
		reason:    opts.Reason,
		expiresAt: now.Add(ttl),
	}
	c.mu.Lock()
	c.rec = rec
	c.mu.Unlock()

	emitQuiesceEvent(ctx, c.audit, rec.setBy, AuditEventTypeQuiesceSet, rec, now)
	return statusFrom(rec, now), nil
}

// Clear deactivates an uncleared flag (active OR auto-released by TTL) — the
// unconditional operator override. Returns ErrQuiesceNotSet only when nothing
// has ever been set or the row was already explicitly cleared; an expired-but-
// uncleared row is still clearable so operators can tidy a crashed deploy's
// flag and emit the quiesce.cleared audit trail.
func (c *MemoryQuiesceController) Clear(ctx context.Context, by string) (*QuiesceStatus, error) {
	now := c.timestamp()
	c.mu.Lock()
	if c.rec == nil || c.rec.clearedAt != nil {
		c.mu.Unlock()
		return nil, ErrQuiesceNotSet
	}
	cleared := now
	c.rec.clearedAt = &cleared
	rec := *c.rec // SetBy stays the original setter; `by` is the clearer (audit principal)
	c.mu.Unlock()

	emitQuiesceEvent(ctx, c.audit, by, AuditEventTypeQuiesceCleared, &rec, now)
	return statusFrom(&rec, now), nil
}

// CreateTable is a no-op for the in-memory controller.
func (c *MemoryQuiesceController) CreateTable(_ context.Context) error { return nil }
