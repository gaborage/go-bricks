# ADR-114: A Consumer That Gave Up Re-subscribing Fails Readiness, Opt-in and Threshold-gated

**Status:** Accepted
**Date:** 2026-09-15
**Issue:** #1666

## Context

The messaging kind's `/ready` probe leased the control-plane publisher and asked it `IsReady()`.
Nothing on that path reads the consume side, so a service whose consumers were all detached — the
broker deleted a queue, the re-subscribe loop had been failing for hours — answered `/ready` with
`200` and stayed in the load balancer while its queues drained into nothing. The publisher-only
verdict was not an oversight so much as the only signal available: before #1618 and the first link
of #1666 the registry kept no per-consumer subscription state at all.

That state now exists: `ConsumerState.GivenUp()` is true for a consumer that is unsubscribed and
whose consecutive re-subscribe failures have reached `consumerResubscribeWarnFromAttempt`, the
threshold at which the re-subscribe log escalates to WARN. The open question was whether, and how,
readiness should act on it. The requesting service's own ADR-0007 argues against broker-aware
readiness precisely because a healthy reconnect would otherwise flap a pod out of rotation and,
under a restart-on-failure policy, into a restart loop.

## Options Considered

**A — fail readiness while any consumer is unsubscribed.** Rejected: that is the restart-loop
objection verbatim. Every routine broker reconnect unsubscribes every consumer for the length of a
flap.

**B — fail readiness once a consumer's re-subscribe failure streak passes a threshold, always on.**
Rejected as a default. It is the right predicate but the wrong rollout: a deployment whose
`readinessProbe` gates traffic on `/ready` would change behavior on upgrade with no key to turn it
back.

**C — the same predicate behind an opt-in key.** Chosen.

**D — a separate `consumers.critical` bit independent of the publisher arm.** Rejected: ADR-066
gives a slot ONE critical bit, decided at describe time and never re-derived by the judge. Two
criticality levels inside one kind would mean either a second slot or a judge that re-reads
configuration per probe.

## Decision

- `messaging.consumers.critical` (bool, absent = false) opts the messaging kind in. Absent means
  today's behavior verbatim: the probe leases the publisher, asks `IsReady()`, and is never
  critical. Plain `bool` with no registered koanf default, matching `cache.critical` after ADR-094.
- When the key is true the messaging slot is critical, and **both** of its arms are critical with
  it — one bit per slot, set once in `describe()`. The publisher arm keeps exactly the flap profile
  `cache.critical` accepted under ADR-094; the consumer arm is threshold-gated and does not flap.
- The **consumer arm is a lease-independent live check**, so it applies in every tenancy mode:
  `Manager.AnyConsumerGivenUp()` is asked before `Manager.Publisher(ctx, "")`, and only if it
  answers false is the lease taken and the publisher's `IsReady()` read, as before. This made
  `probeDescription.live` mean what its name says: the judge used to DISCARD `live` whenever
  `acquire` was set, and now runs it first and, on its error, instead of acquiring. That matters
  because the judge short-circuits a `NotConfigured` lease to `per_tenant` with a nil error —
  under `multitenant.enabled` with per-tenant tenancy and no root `messaging:` block the lease
  resolves to nothing, so an arm reachable only through the lease would be unreachable in exactly
  the deployment holding the most consumers. A healthy per-tenant kind still reports `per_tenant`
  and `200`; no shipped slot sets both fields, so no existing verdict moves.
- The predicate is a **manager-side door**, `Manager.AnyConsumerGivenUp()`, not a fold over
  `ConsumerStates()` in `app`. `/ready` asks on every poll, and a snapshot would copy every
  declared consumer's row — four identifier strings apiece, per tenant key — to answer one bool,
  carrying coordinates `ConsumerState`'s own contract keeps out of the unauthenticated body into
  the package that renders it. The door allocates nothing and stops at the first consumer that
  has given up.
- The threshold is the WARN constant, not a second number. One threshold, one meaning: the point at
  which the supervisor has stopped looking like it is riding out a flap. A consumer whose channel
  just closed stays ready, and that it is unsubscribed shows in `messaging_stats` — and in
  `/_sys/health-debug`, which renders the same map — as `subscribed_consumers` below
  `declared_consumers`, never in the verdict. The streak COUNT itself is published nowhere: it
  is readable through `Registry.ConsumerStates()`/`Manager.ConsumerStates()` in Go, and reached
  `/ready` through no key this decision adds.
- ADR-048 governs the body: the sentinel carries no queue name, consumer tag or event type, the
  slot declares no `PublicErr`, and the unauthenticated `503` renders the default
  `messaging unavailable`.
- Two semantics settled while building the state this decision reads, both of which the verdict
  depends on. **Shutdown mask:** a stopped registry reports every consumer unsubscribed with no
  streak, so shutting down is never an outage. **Session boundary:** every `StartConsumers` installs
  fresh per-consumer state, so a restarted consumer's first flap is judged on its own attempts, not
  on a previous session's streak. The cumulative counters survive both.
- `ManagerOptions.ConsumerResubscribeDelay` (Go-only, no YAML key; zero keeps the registry's 5s
  floor) is added so a test can drive a real streak in milliseconds. It follows
  `ConnectionTimeout`'s idiom and reaches every registry the manager builds.

## Consequences

**Positive:** a service whose consumers are detached can be taken out of rotation by its own
readiness probe, with one greppable key and no change for anyone who does not set it. The threshold
answers the restart-loop objection: a reconnect that recovers within the streak never reaches the
verdict.

**Negative:** with the key on, a genuine broker outage long enough to exhaust the streak takes the
pod out of rotation even though nothing is wrong with the pod — which is the point, and is why it is
opt-in. Operators who gate `livenessProbe` on `/ready` will restart such a pod; gate liveness on
`/health`, which is static.

The same mechanism is reachable deliberately, and the threat model should be read before enabling
the key: anyone who can make a consumer's re-subscribe fail five times running — deleting its
queue, revoking `consume` on it, or an argument change answering `PRECONDITION_FAILED`, which
ADR-113 skips until the process restarts so the streak never recovers in-process — takes every
replica out of the load balancer at once. The key therefore widens what a broker credential
reaches: not only "this service stops consuming" but "this service stops serving HTTP". That is the
trade the key exists to make, and it is why it is opt-in rather than the default.

**Neutral:** `Manager.StopConsumers()` has one production caller (`app/lifecycle.go:464`, shutdown),
so the "consumers are never revived after a stop" behavior sits entirely inside a closing process
and cannot produce a readiness verdict. That `StopConsumers` cancels its supervisors without joining
them is deliberate (ADR-029) and is the reason the state layer masks a stopped registry rather than
clearing it; whether to join them is tracked as **#1685**, out of scope here.

## References

- #1666 — this decision; #1618 — the shared WARN threshold; #1685 — `StopConsumers` does not join
  its supervisors
- `config/types.go` — `MessagingConsumersConfig`; `config/config.go` — `IsMessagingConsumersCritical`
- `app/slot.go` — `messagingSlot.describe`; `app/readiness.go` — `errConsumerResubscribeExhausted`
- `messaging/registry.go` — `ConsumerState.GivenUp`, `consumerResubscribeWarnFromAttempt`
- [ADR-029](adr_029_graceful_shutdown_order.md), [ADR-048](adr_048_ready_sanitize_by_default.md),
  [ADR-066](adr_066_readiness_one_module.md), [ADR-094](adr_094_cache_readiness_non_critical_default.md),
  [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md)
- [messaging.md](messaging.md), [startup_defaults.md](startup_defaults.md),
  [migrations.md](migrations.md) `[C65.12]`
