# Consumer-registered readiness contributions

**Decision:** Deferred (YAGNI) — `/ready` keeps walking the framework's own
slot list and nothing else. There is no `RegisterReadiness(name, critical,
Prober)` door, and `app.Prober` stays implemented by the framework's probe
descriptions alone (ADR-066 as amended 2026-09-06), until a trigger below
fires.

**Reason (revised 2026-09-28 for ADR-120):** ADR-066 made readiness one machine
on purpose: one status vocabulary, one gate, and one body rule. ADR-120 then
trimmed that body to the verdict alone (`{"status":"ready"}` /
`{"status":"not ready"}`), which dissolved two of the three invariants this
entry originally rested on. One remains, and `Name` picked up a new constraint
that did not exist when the entry was written:

1. ~~**The name reaches the unauthenticated 503 body.**~~ **Dissolved by
   ADR-120** — no name reaches either body, and ADR-048's `<name> unavailable`
   text is gone with the `PublicErr` seam that produced it. What survives is
   weaker and not a disclosure decision: `Name` is still the `readiness.kind`
   attribute on the `app.readiness.status` gauge, the `data.components.<name>` key on
   `/_sys/health-debug`, and the `component=` log field. **New constraint:** as
   a metric dimension its cardinality is now bound — a consumer-chosen name must
   be a fixed identifier, never a tenant, host or database name, or a
   contribution silently multiplies time series. The reserved-name guard
   (`database`, `messaging`, `cache`, `streams`, `readiness`) survives too, but
   as collision avoidance on a map key and a gauge attribute, not as a
   sanitisation rule.
2. ~~**The 200 body renders statistics only through an allowlist.**~~
   **Dissolved by ADR-120** — the allowlist (`probeDescription.publicStats` and
   the four per-kind lists) no longer exists. A contribution's `Details` map
   would land only behind the debug endpoint's own gates (`debug.enabled` plus
   `debug.allowedips` or `debug.bearertoken`), so there is no render-seam policy
   for the door to carry.
3. **A consumer-chosen `critical` bit pulls the service out of the load
   balancer.** *Unchanged, and now the whole of the invariant case.* Where a
   contribution sits in the gate's short-circuit order relative to the four
   framework slots is a design decision, not a default. The short-circuit also
   means a contribution placed ahead of a framework slot can stop that slot
   being judged at all, which now costs its gauge series freshness as well as
   its verdict.

**The deferral stands all the same, because the reason that actually carried it
is untouched:** the request that motivated the door (#1617) turned out not to need it. Its
case was a dead AMQP consumer channel leaving the process ready-green. That
is two framework defects, not a missing door: topology is never re-declared
after a reconnect, so a consumer can loop on `404 NOT_FOUND` forever and
silently (#1618), and the consumer supervisor tracks no subscription state
for readiness to read (#1666). Both are fixed inside the framework's own
messaging slot. The requesting service's own architecture record also
keeps consumer readiness deliberately broker-blind, because failing
readiness during a reconnect turns the reconnect into a restart loop and
takes the queue's only consumer away while it recovers. So the one
concrete caller would leave the door unused.

What a consumer with a genuinely foreign dependency can do today:
`Server.RegisterReadyHandler` replaces `/ready` wholesale. It is a
replacement, not an extension: the handler loses the slot walk and the gate,
and — because verdict recording lives inside the handler it replaced — a
`/ready` poll then feeds neither `app.readiness.status` nor the non-critical
`Readiness component unhealthy` WARN, leaving `/_sys/health-debug` as the only
thing that still advances them. The stats allowlist and ADR-048's sanitisation
dropped off this list with ADR-120: there is nothing left to lose there. The
path is documented in `wiki/observability.md` so the cost is visible rather
than hidden.

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
amendment — and ADR-120's body contract — in a new ADR that resolves the
surviving invariant (3) and the `Name` cardinality constraint by name; a
contribution rides the slot list as a fifth slot kind with no start or stop
lifecycle; its verdict must be recordable by `verdictStore`, since that is now
the only way a kind's status leaves a `/ready` request at all; readiness stays
in `app/` (the judge and its types are unexported there, so a new leaf
`health/` package risks an import cycle); `/health` is never affected.

**Prior requests:**

- [#1617](https://github.com/gaborage/go-bricks/issues/1617) — closed
  2026-09-14 (deferred, this entry; consumer-state half split to #1666)
