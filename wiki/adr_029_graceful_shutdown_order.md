# ADR-029: Graceful Shutdown Phase Ordering (Stop Inbound Work Before Teardown)

**Status:** Accepted
**Date:** 2026-06-10

> **Amended (2026-08-29, the observability phase is best-effort):** phase 4 no longer
> contributes to the error `App.Shutdown` — and therefore `App.Run()` — returns. A provider
> `Shutdown` failure of any kind is logged once at WARN with the error and the phase duration,
> and the walk proceeds to the closers; only the ORDER this ADR fixed was ever load-bearing,
> and it is unchanged. The fold could not be satisfied with the framework's own defaults: the
> batch processor's in-flight export runs on a context of the SDK's own, bounded by
> `observability.*.export.timeout` (10s in `development`, 60s otherwise), while the phase's
> budget is whatever the HTTP drain, the slot stop and module shutdown left of the one
> `App.Shutdown` context (`server.timeout.shutdown`, 10s) — every phase ahead of it spends
> from the same budget, so a slow module shortens the flush as surely as a slow drain does. A collector that
> is down therefore failed a graceful shutdown in which nothing but the telemetry sink had
> failed. `App.Run()`'s error now reflects application failures — server, modules, closers, the
> hard-stop timeout — not the availability of the telemetry sink. See `[C61.20]` in
> [migrations.md](migrations.md) and issue #1225.
>
> **Superseded in part by [ADR-067](adr_067_lifecycle_slots.md) (2026-08-17):** phase 5's separate
> "manager cleanup loops" step no longer exists. `DbManager` and `messaging.Manager` start idle
> cleanup at construction and stop it inside their own `Close()`, which the closers run — so the
> ordering this ADR established is preserved, with one fewer phase to keep in sync.

## Context

`App.Shutdown` tore phases down in this order:

1. **modules** (`registry.Shutdown()`)
2. HTTP server (`server.Shutdown`)
3. observability flush/shutdown
4. manager cleanup loops
5. closers (DB pools, **messaging connections — which is where consumers were stopped**, inside `Manager.Close()`)

So modules were shut down **first**, while the HTTP server was still serving requests and AMQP consumers were still delivering messages. In-flight HTTP handlers and message handlers then ran against **already-shut-down modules** — nil services, closed caches, released resources — producing panics and errors precisely during the shutdown window (a High finding from the 2026-06-10 audit). The longer module/observability/closer teardown took, the wider the window in which consumers kept handing work to dead modules.

## Decision

Reorder `App.Shutdown` to stop **inbound work first**, then tear down what it depends on:

1. **HTTP server** — stop accepting new requests; drain in-flight handlers.
2. **AMQP consumers** — stop delivering new messages (new `App.shutdownConsumers()` → `Manager.StopConsumers()`), *without* closing connections.
3. **modules** — no new HTTP requests or AMQP deliveries are admitted; in-flight handlers may still be unwinding after cancellation, but no fresh work is handed to modules being torn down.
4. **observability** — flush and shut down, best-effort: failures are warned, never folded into the shutdown error (2026-08-29 amendment above).
5. **closers** (DB pools, messaging connections). Manager cleanup loops were a separate phase
   here until [ADR-067](adr_067_lifecycle_slots.md); each manager now stops its own sweep in
   `Close()`, which the closers still run last.

`Manager.StopConsumers()` is a new public method that cancels each consumer registry's consume context (idempotent — `Registry.StopConsumers` guards on its active flag) without closing the AMQP clients. `Manager.Close()` (run later via the messaging-manager closer) still stops consumers and closes connections, so the two compose safely.

## Consequences

**Behavioral change (not an API break):**

- Shutdown now drains the HTTP server and stops consumers **before** modules are torn down. Applications whose module `Shutdown()` implicitly relied on the server still serving, or on consumers still running, will see the corrected order. No application code must change; `Manager.StopConsumers` is purely additive.
- The framework stops handing **new** HTTP requests and AMQP messages to modules before they shut down — closing the dominant race (a message pulled and handled entirely against a shut-down module during a slow shutdown). `Manager.StopConsumers` cancels each consumer's context, which propagates to in-flight handlers, but does **not** synchronously join them; a handler already executing at the moment of cancellation may still briefly overlap module teardown. A fully synchronous drain (joining worker goroutines, with a bounded deadline so a stuck handler cannot hang shutdown) is possible future work.

**Additive API:**

- `messaging.Manager` gains `StopConsumers()` for callers that want to quiesce consumers without tearing down the manager.

See [migrations.md](migrations.md) for the operator-facing note.
