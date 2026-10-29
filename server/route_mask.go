package server

import (
	"maps"
	"sync"
	"sync/atomic"
)

// maskToken stands in for every path-parameter value and wildcard tail, and is the whole
// rendering of an empty path or a template the table never learned.
const maskToken = "REDACTED"

// maskTable maps a route template to its masked rendering, so an inbound path can be printed
// from its matched template with each parameter value replaced by maskToken. Writes copy the
// map and publish the copy, so render never takes a lock and allocates nothing. The zero value
// is ready to use.
type maskTable struct {
	mu      sync.Mutex // serializes writers; readers only load
	entries atomic.Pointer[map[string]string]
}

// learn records masked as the rendering of key, stored verbatim. Learning the rendering a key
// already holds publishes nothing.
func (t *maskTable) learn(key, masked string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	old := t.entries.Load()
	var next map[string]string
	if old != nil {
		if cur, ok := (*old)[key]; ok && cur == masked {
			return
		}
		next = maps.Clone(*old)
	} else {
		next = make(map[string]string, 1)
	}
	next[key] = masked
	t.entries.Store(&next)
}

// render returns the masked rendering learned for path, or maskToken when path is empty or
// was never learned: an unknown template fails closed.
func (t *maskTable) render(path string) string {
	if path == "" {
		return maskToken
	}
	if m := t.entries.Load(); m != nil {
		if masked, ok := (*m)[path]; ok {
			return masked
		}
	}
	return maskToken
}
