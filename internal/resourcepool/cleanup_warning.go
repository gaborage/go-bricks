package resourcepool

import (
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// WarnIfCleanupIntervalTooLate WARNs (never fails) when a pool's sweep runs no more often than
// its idle TTL, so an idle handle lingers up to one extra cycle. It lives beside the pool that
// owns both values — not in config.Validate, which has no logger — and is shared by the
// managers that emit it so the message cannot drift between them. keyPrefix is the
// operator-facing config prefix, e.g. "database.manager" or "messaging.publisher".
// A non-positive idleTTL disables idle cleanup outright, so there is nothing to lag behind.
func WarnIfCleanupIntervalTooLate(log logger.Logger, keyPrefix string, cleanupInterval, idleTTL time.Duration) {
	if idleTTL <= 0 || cleanupInterval < idleTTL {
		return
	}
	log.Warn().
		Str("resource", keyPrefix).
		Dur("cleanupinterval", cleanupInterval).
		Dur("idlettl", idleTTL).
		Msg(keyPrefix + ".cleanupinterval is >= " + keyPrefix + ".idlettl; " +
			"idle handle eviction will lag by up to one extra cleanup cycle " +
			"(lower " + keyPrefix + ".cleanupinterval or raise " + keyPrefix + ".idlettl)")
}
