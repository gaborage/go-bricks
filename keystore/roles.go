package keystore

import (
	"slices"
	"sync"
)

// Role tags a startup resolution carries: which framework feature asked the store
// for an entry. HTTP jose (a route policy) and payload sealing (a seal-tagged event
// type) must never share a kid — one key serving two protocols widens what a
// compromise of either reaches — so the store remembers the tag of every startup
// resolution and the app WARNs once per entry seen under both (#1306, ADR-097).
// Warn only: an enforced prefix partition was rejected as breaking shipped HTTP
// surface. Runtime (per-message) resolutions never record a tag. The app reads the
// log once route registration is complete, so a NEW startup resolution path must run
// before that point (app.prepareRuntime) or its entries go unreported.
const (
	RoleTagJoseRoute = "jose-route"
	RoleTagSeal      = "seal"
)

// RoleRecorder is the optional door a startup resolver uses to tag an entry. The
// keystore module's store implements it; a test double may too.
type RoleRecorder interface {
	RecordResolution(entry, role string)
}

// DualRoleReporter is the optional door the app reads after registration: every
// entry resolved under more than one role tag, mapped to the sorted tags it was
// seen under. Plain map so the app (which keystore imports) can name the method
// without importing this package.
type DualRoleReporter interface {
	DualRoleEntries() map[string][]string
}

// roleLog is the store's per-entry tag set. It is written only during startup
// resolution, from whichever module Init reaches it, so it carries its own lock.
type roleLog struct {
	mu    sync.Mutex
	roles map[string]map[string]struct{}
}

// RecordResolution implements RoleRecorder: idempotent per (entry, role), safe for
// concurrent module Init. An unknown entry is recorded too — the resolution that
// named it already failed startup on its own, and the log stays a pure record.
func (l *roleLog) RecordResolution(entry, role string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.roles == nil {
		l.roles = make(map[string]map[string]struct{})
	}
	tags := l.roles[entry]
	if tags == nil {
		tags = make(map[string]struct{})
		l.roles[entry] = tags
	}
	tags[role] = struct{}{}
}

// DualRoleEntries implements DualRoleReporter: entries with two or more tags,
// each tag list sorted, so a report is deterministic once the caller orders the keys.
func (l *roleLog) DualRoleEntries() map[string][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string][]string{}
	for entry, tags := range l.roles {
		if len(tags) < 2 {
			continue
		}
		roles := make([]string, 0, len(tags))
		for role := range tags {
			roles = append(roles, role)
		}
		slices.Sort(roles)
		out[entry] = roles
	}
	return out
}

var (
	_ RoleRecorder     = (*store)(nil)
	_ DualRoleReporter = (*store)(nil)
)
