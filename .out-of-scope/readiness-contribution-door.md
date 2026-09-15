# Consumer-registered readiness contributions

**Decision:** Deferred (YAGNI) — `/ready` keeps walking the framework's own
slot list and nothing else. There is no `RegisterReadiness(name, critical,
Prober)` door, and `app.Prober` stays implemented by the framework's probe
descriptions alone (ADR-066 as amended 2026-09-06), until a trigger below
fires.

**Reason:** ADR-066 made readiness one machine on purpose: one status
vocabulary, one gate, one body rule, and a per-kind public-stats allowlist
enforced at the render seam. A foreign contribution punches through three of
its invariants at once, each of which is a security or availability decision
rather than a plumbing one:

1. **The name reaches the unauthenticated 503 body.** `/ready` carries no
   authentication and no IP allowlist, and `<name> unavailable` is
   interpolated into its body (ADR-048). Framework names are fixed
   identifiers; a consumer-chosen name needs validation plus a reserved-name
   guard (`database`, `messaging`, `cache`, `streams`, `readiness`).
2. **The 200 body renders statistics only through an allowlist.** A consumer
   `Details` map has none, so a contribution would render either status-only
   (the existing fallback) or through a mandatory allowlist argument. Either
   is a policy the door would have to carry.
3. **A consumer-chosen `critical` bit pulls the service out of the load
   balancer.** Where a contribution sits in the gate's short-circuit order
   relative to the four framework slots is a design decision, not a default.

The request that motivated the door (#1617) turned out not to need it. Its
case was a dead AMQP consumer channel leaving the process ready-green. That
is two framework defects, not a missing door: topology is never re-declared
after a reconnect, so a consumer can loop on `404 NOT_FOUND` forever and
silently (#1618), and the consumer supervisor tracks no subscription state
for readiness to read (#1666). Both are fixed inside the framework's own
messaging slot. The requesting service's own architecture record
(`cifra-token-api-go` ADR-0007) also keeps consumer readiness deliberately
broker-blind, because failing readiness during a reconnect turns the
reconnect into a restart loop and takes the queue's only consumer away while
it recovers. So the one concrete caller would leave the door unused.

What a consumer with a genuinely foreign dependency can do today:
`Server.RegisterReadyHandler` replaces `/ready` wholesale. It is a
replacement, not an extension: the handler loses the slot walk, the gate,
the stats allowlist and ADR-048's sanitisation, and must re-implement
whatever of those it still wants. That path is documented in
`wiki/observability.md` so the cost is visible rather than hidden.

**Reopen when either fires:**

1. A consumer names a concrete non-framework dependency that must gate
   traffic (a partner endpoint, an HSM or keystore, a warmed coordinate
   cache, a schema-version check) and cannot express it as a framework
   kind.
2. A consumer ships a `RegisterReadyHandler` replacement and re-implements
   the slot walk to keep the framework's verdict, proving the cost of the
   replacement path is real.

A second speculative request is not a trigger on its own.

**Aiming constraints for any future door:** it supersedes the ADR-066
amendment in a new ADR that resolves the three invariants above by name; a
contribution rides the slot list as a fifth slot kind with no start or stop
lifecycle; readiness stays in `app/` (the judge and its types are unexported
there, so a new leaf `health/` package risks an import cycle); `/health` is
never affected.

**Prior requests:**

- [#1617](https://github.com/gaborage/go-bricks/issues/1617) — closed
  2026-09-14 (deferred, this entry; consumer-state half split to #1666)
