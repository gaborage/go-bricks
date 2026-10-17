# Re-creating a stream the broker lost

**Decision (2026-10-17):** Deferred (YAGNI) — the streams lane does not
re-declare a stream or super stream the broker lost, rebuild its consumers and
publishers, or reset their offsets while the process runs, not even on
opt-in. This is a deferral, not a rejection: it waits for a trigger below.

**What the framework does today
([ADR-123](../wiki/adr_123_streams_lost_topology_supervisor.md)):** the
manager's supervisor reads every handle's status every 5s. A handle the client
gave up on is reported once at ERROR, naming the stream and the consumer or
publisher. The non-critical `streams` component stays unhealthy, and the lost
consumer's shutdown flush is skipped. A restart is the recovery. It re-declares
the stream from the service's declarations, and each consumer falls back to
its declared `Start`. A restarted replica does not revive its peers, so every
replica that logged the ERROR restarts. See
[streams.md](../wiki/streams.md#a-lost-stream).

**Reason:**

1. **No demand.** The only use case on record is the maintainer's demo
   project. After a broker wipe, one of its consumers stayed absent until the
   app restarted (#1797), and a restart already recovers that.
2. **Cost.** The draft that failed review added about 1,400 lines, about
   1,200 of them Go, and it still lacked the fences below. That is a
   four-link stack for a feature nobody else has asked for.
3. **The client cannot be stopped mid-retry.** In rabbitmq-stream-go-client
   v1.9.0, only the reliable super-stream consumer's `Close` interrupts a
   pending reconnect retry: `retry()` takes its stop signal from an unexported
   hook only that type implements. Closing a plain consumer mid-retry leaves
   the retry running. It re-subscribes at its old position, then closes the
   new subscription. Closing a plain or super-stream producer mid-retry is
   worse: the retry reopens it and marks it open again. So rebuilding a handle
   the client is still retrying races the client. Upstream
   [rabbitmq/rabbitmq-stream-go-client#535](https://github.com/rabbitmq/rabbitmq-stream-go-client/pull/535)
   ("Stop reconnection when a reliable entity is closed while reconnecting")
   would close this gap for all four handle types. It is still open.
4. **Some risks remain that no in-process fence can close.**
   - *A peer reattaches at its old position.* A replica's consumer may still
     have a retry pending when another replica re-declares the stream. That
     consumer then reattaches to the new, empty stream at its old position:
     the last offset + 1 for a plain consumer, or its per-partition position
     map for a super stream. It reports open, so its own supervisor never
     flags it. It silently skips the start of the new stream and commits high
     offsets under the shared consumer name. The client's retry recursion has
     no attempt cap, so no grace period bounds this.
   - *A re-declare resurrects a deliberate delete.* An operator sometimes
     deletes a stream on purpose. For the same reason, ADR-123 rejects
     re-creation by default.
   - *A partial super-stream loss cannot be repaired.* The client's
     `DeclareSuperStream` treats `StreamAlreadyExists` as success. A re-declare
     of a super stream that lost only some partitions therefore succeeds
     without restoring them.

The offset-book generation seam (`offsetBook.reset()` and the handle
generation that `recordSettled` checked), added in #1829 as groundwork, had no
production caller. It was removed along with this deferral. A future design
should not start from it: the generation check did not fence a settlement
already in flight (#1826, finding 4).

**Alternatives considered:**

- **A / B: re-declare in process.** A covers plain streams only and refuses
  super streams at `Start`. B covers super-stream consumers too. Both carry
  every residual risk above, and B still cannot repair a partial loss.
- **E: reattach on reappearance.** Never re-declare. Poll the lost topology
  and rebuild from `Start` once an operator, or a restarted replica,
  re-creates it. This removes the resurrection and concurrent re-declares. It
  still needs a close-once rule, action only on closed handles, and the
  in-flight fence.
- **F: restart on loss.** Give streams ADR-114's opt-in critical shape
  (`messaging.consumers.critical`), or wire the loss to liveness, so the
  orchestrator restarts the pod. F needs no vendor surface and no offset
  reset, and it reuses the restart recovery documented today. But readiness
  alone only drains the pod, and ADR-123 put "make streams critical" out of
  scope, so F needs its own ADR.
- **D: restart the component inside the manager.** Close the environment and
  `Start` again. D avoids an offset reset by construction. But it drops every
  healthy handle to repair one stream, and it reverses ADR-123's rejection of
  calling `Start` again.

**Reopen when either fires:**

1. A second consuming service, besides the demo project, asks to recover a
   lost stream without a restart.
2. Upstream rabbitmq-stream-go-client#535 ships in a release, so `Close`
   cancels the pending reconnect retry of every reliable handle.

**Aiming constraints for any future design:** re-check #1826's four findings
against the pinned client. Act only on handles the client has settled to
closed, and count `AlreadyClosed` as a successful close. Before any offset
reset, drain the old handle's in-flight handler and commit work, with a bound.
Derive any grace period from `reconnectBackOff` and pin it with a test. Keep
refusing a held consumer together with re-creation at `Start`: the hold ledger
keys a row by consumer, stream and offset, and a re-created stream numbers its
offsets from 0 again.

**Prior requests:**

- [#1826](https://github.com/gaborage/go-bricks/issues/1826) — deferred
  2026-10-17 (this entry)
