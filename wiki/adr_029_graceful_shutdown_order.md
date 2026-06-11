# ADR-029: Graceful Shutdown Phase Ordering (Stop Inbound Work Before Teardown)

**Status:** Accepted
**Date:** 2026-06-10

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
4. **observability** — flush and shut down.
5. **manager cleanup loops**, then **closers** (DB pools, messaging connections).

`Manager.StopConsumers()` is a new public method that cancels each consumer registry's consume context (idempotent — `Registry.StopConsumers` guards on its active flag) without closing the AMQP clients. `Manager.Close()` (run later via the messaging-manager closer) still stops consumers and closes connections, so the two compose safely.

## Consequences

**Behavioral change (not an API break):**

- Shutdown now drains the HTTP server and stops consumers **before** modules are torn down. Applications whose module `Shutdown()` implicitly relied on the server still serving, or on consumers still running, will see the corrected order. No application code must change; `Manager.StopConsumers` is purely additive.
- The framework stops handing **new** HTTP requests and AMQP messages to modules before they shut down — closing the dominant race (a message pulled and handled entirely against a shut-down module during a slow shutdown). `Manager.StopConsumers` cancels each consumer's context, which propagates to in-flight handlers, but does **not** synchronously join them; a handler already executing at the moment of cancellation may still briefly overlap module teardown. A fully synchronous drain (joining worker goroutines, with a bounded deadline so a stuck handler cannot hang shutdown) is possible future work.

**Additive API:**

- `messaging.Manager` gains `StopConsumers()` for callers that want to quiesce consumers without tearing down the manager.

See [migrations.md](migrations.md) for the operator-facing note.
