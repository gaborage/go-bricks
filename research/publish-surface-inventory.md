# Publish-surface inventory — blast radius of the #1305 typed-publisher break

Input to #1309 (ADR, `wiki/migrations.md` atom, breaking-changes skill line). Facts and
pointers only; no recommendations. Pointers are against `origin/main` at `64db05a3`
(2026-09-03). Demo project pointers are against `gaborage/go-bricks-demo-project` at
`13592b3f`, which pins `go-bricks v0.60.0`.

The decision under inventory ([#1305 comment, 2026-09-03](https://github.com/gaborage/go-bricks/issues/1305#issuecomment-5519911579)):
`DeclareTypedPublisher[T]` → `Publisher[T]` becomes the only module-facing publish door;
raw `[]byte` `Publish`/`PublishToExchange` leave the consumer surface with no escape hatch;
framework internals keep moving bytes.

## 1. Exported symbols that let a module publish bytes today

| Symbol | Defined at | Shape | Framework callers (must survive as internal plumbing) | What a module author reaches it through |
|---|---|---|---|---|
| `messaging.Client.Publish` | `messaging/messaging.go:14-18` | `Publish(ctx, destination string, data []byte) error` on the base `Client` interface | `messaging/amqp_client.go:299` (impl, delegates to `PublishToExchange` with `Exchange:""`); `messaging/stamping_publisher.go:33` (tenant-stamp wrapper) | `scheduler.JobContext.Messaging()` returns `messaging.Client` (`scheduler/job.go:61`, `:131`); `app.ModuleDeps.Messaging` returns `AMQPClient`, which embeds `Client` |
| `messaging.AMQPClient.PublishToExchange` | `messaging/messaging.go:58-62` | `PublishToExchange(ctx, options PublishOptions, data []byte) error` | `messaging/amqp_client.go:428` (impl, retry loop, ADR-033 bound); `messaging/stamping_publisher.go:37-58` (wrapper); `outbox/relay.go:453` (relay drains `record.Payload` bytes) | `app.ModuleDeps.Messaging func(ctx) (messaging.AMQPClient, error)` (`app/module.go:288`); README `:340`, `:840` |
| `messaging.PublishOptions` | `messaging/messaging.go:34-40` | `{Exchange, RoutingKey string; Headers map[string]any; Mandatory, Immediate bool}` — already non-comparable (map field) | `outbox/relay.go:445-449` builds one per record; `stamping_publisher.go:34` | Named in every `PublishToExchange` call site in docs |
| `messaging.Manager.Publisher` | `messaging/manager.go:365` | `Publisher(ctx, key string) (AMQPClient, ReleaseFunc, error)` — pooled client lease | `app/app.go:321`, `app/prewarm.go:42`, `app/readiness.go:249`, `app/resource_provider.go:134`, `:255`, `app/slot.go:177` | Not reached directly by modules; it is what `ModuleDeps.Messaging` resolves through |
| `messaging.Declarations.DeclarePublisher` / `NewPublisher` / `PublisherDeclaration` | `messaging/helpers.go:282`, `:81`; `messaging/registry.go:128` | Declaration only — registers `PublisherOptions{Exchange, RoutingKey, EventType, Description, Headers, Mandatory, Immediate}` (`messaging/helpers.go:69-77`). It hands back no publish handle; the bytes go through `AMQPClient` separately | `messaging/declarations.go:214` (replay copy), `:563` (validate), `:651` (hash); `messaging/registry.go:240-275` (register + duplicate check); `Registry.Publishers()` `registry.go:922` | `decls.DeclarePublisher(...)` in `DeclareMessaging` (llms.txt `:2953`, wiki/messaging.md `:24`, `:44`) |
| `streams.Publisher.Publish` | `messaging/streams/publisher.go:238` | `Publish(ctx, msg *PublishMessage) error`; `PublishMessage{Data []byte; Properties map[string]any; RoutingKey string}` (`messaging/streams/streams.go:45-57`) | `outbox/relay.go:532` (super-stream lane) | `decls.DeclarePublisher(&streams.PublisherOptions{...}) *Publisher` (`messaging/streams/declarations.go:253`); `DeclareSuperStreamPublisher` (`:267`). Handle is returned at declaration time, unlike the AMQP lane |
| `app.OutboxPublisher.Publish` / `app.OutboxEvent.Payload` | `app/module.go:104-108`; `:124-150` | `Payload any` — "If `[]byte`, stored as-is. Otherwise, JSON-marshaled" (`:131`) | `outbox/publisher.go:37`, `outbox/module.go:425` (lazy wrapper); relay reads `record.Payload` bytes at `relay.go:453`, `:532` | `deps.Outbox.Publish(ctx, tx, &app.OutboxEvent{...})` (`app/module.go:254`) |
| `testing/mocks.MockMessagingClient.Publish`, `MockAMQPClient.PublishToExchange` | `testing/mocks/messaging.go:42`, `testing/mocks/amqp.go:44` | Exported consumer-test doubles implementing the two interfaces above | none (test surface) | Consumer test suites that inject them |

Notes on the table:

- `messaging.Client` is the base interface; `AMQPClient` embeds it (`messaging.go:59`). Removing
  `Publish` from the module surface touches both the AMQP accessor (`ModuleDeps.Messaging`) and
  the scheduler accessor (`JobContext.Messaging()` typed as `Client`).
- The stamping wrapper (`stamping_publisher.go`) is where the ADR-087 `x-tenant-id` stamp is
  written; it sits between the module-facing handle and `AMQPClientImpl`.
- `messaging/internal/payloaderr` is `internal/` — auto-ignored by apidiff and not importable
  from `messaging/streams` or a consumer.

## 2. Docs and examples that teach the raw door

Counted as a raw-door call site: a code sample or table cell that shows a module calling
`client.Publish(...)` / `client.PublishToExchange(...)` or `streams.Publisher.Publish(&PublishMessage{Data: ...})`.
Prose that describes retry/readiness behavior of those methods is listed separately (it
needs rewording, not deletion).

### 2a. AMQP raw door — framework docs

| File:line | What it shows | Kind |
|---|---|---|
| `llms.txt:3169` | "The `messaging.AMQPClient` returned by `deps.Messaging(ctx)` exposes two publish APIs" | prose |
| `llms.txt:3173` | table row `client.PublishToExchange(ctx, PublishOptions{...}, body)` — "Routed publishing (preferred)" | call site |
| `llms.txt:3174` | table row `client.Publish(ctx, destination, body)` — default-exchange send | call site |
| `llms.txt:3220` | `PublishOrderCreated` sample: `json.Marshal(evt)` then `client.PublishToExchange(...)` | call site |
| `llms.txt:3237` | `NotifyDirectQueue` sample: `client.Publish(ctx, queueName, body)` | call site |
| `llms.txt:3393` | cheatsheet row "Publish — routed (preferred)" | call site |
| `llms.txt:3394` | cheatsheet row "Publish — default exchange" | call site |
| `llms.txt:5277` | tracing table: "Every `Publish()`/`PublishToExchange()` call" emits the publish span | prose |
| `wiki/messaging.md:549` | "`PublishToExchange` (and the `Publish` convenience) retries a failing publish" | prose |
| `wiki/messaging.md:607` | outbox parking vs "calling `PublishToExchange`" | prose |
| `wiki/messaging.md:624`, `:629` | readiness pre-flight described on `PublishToExchange` | prose |
| `wiki/context_deadlines.md:20` | timeout table row: "one `PublishToExchange` at default backoffs" | prose |
| `README.md:340`, `:840` | `ModuleDeps.Messaging` returns `messaging.AMQPClient`; "Modules access ... `deps.Messaging(ctx)`" | accessor type, no publish call |
| `CLAUDE.md` | no `Publish(`/`PublishToExchange(` mention; the Context Deadlines table names "AMQP publish (call bound)" | prose |
| `wiki/adr_033_*.md`, `wiki/adr_040_*.md:65`, `:113` | historical ADR text naming the methods | ADR (immutable) |
| `examples/` | directory does not exist | — |
| `docs/superpowers/plans/*` | plan transcripts naming the methods (`2026-08-16-streams-environment-port.md`, `2026-08-29-*`) | scratch, not consumer-facing |

**AMQP raw-door call sites in consumer-facing framework docs: 6** (all in `llms.txt`:
`3173`, `3174`, `3220`, `3237`, `3393`, `3394`). `wiki/messaging.md` teaches only the
declaration side (`:24`, `:44`) and describes the methods in prose (4 lines).

### 2b. Streams raw door — framework docs

| File:line | What it shows |
|---|---|
| `llms.txt:3147` | `m.pub = decls.DeclarePublisher(&streams.PublisherOptions{Stream: "orders"})` |
| `llms.txt:3154`, `:3160` | `m.pub.Publish(ctx, &streams.PublishMessage{...})` |
| `wiki/streams.md:416` | `decls.DeclarePublisher(&streams.PublisherOptions{Stream: "orders"})` |
| `wiki/streams.md:425`, `:433` | `m.orders.Publish(ctx, &streams.PublishMessage{...})`, `m.payments.Publish(...)` |
| `wiki/adr_063_streams_native_publishing.md:36` | `Publisher.Publish(ctx, *PublishMessage) error` contract (ADR, immutable) |

**Streams raw-door call sites in consumer-facing docs: 4** (`llms.txt:3154`, `:3160`,
`wiki/streams.md:425`, `:433`). #1305 names streams only as "future streams use" of
`h.Seal()` and says streams typed declarations hard-reject seal-tagged `T` in v1; it does not
say whether `streams.Publisher.Publish` leaves the surface.

### 2c. Outbox — framework docs (struct payload; stays per #1305, `[]byte` is the "documented residual")

`README.md:712`; `llms.txt:3395`, `:3454`, `:3472`, `:3483`, `:3508`, `:3512`; `wiki/outbox.md:57`,
`:72`, `:156`, `:209`, `:457`; `wiki/migration_provisioning.md:268`; `wiki/adr_041_*.md:102`, `:140`.
All pass a struct or map `Payload`; none pass `[]byte`.

### 2d. Demo project (`gaborage/go-bricks-demo-project` @ `13592b3f`, `go-bricks v0.60.0`)

| File:line | What it does |
|---|---|
| `internal/modules/products/module.go:70-77` | `DeclareMessaging` registers the `product-events` topic exchange only — no `DeclarePublisher`, no consumer |
| `internal/modules/products/service/service.go:101` | `s.outbox.Publish(ctx, tx, &app.OutboxEvent{EventType: "product.created", Payload: product})` — struct |
| `internal/modules/products/service/service.go:292` | outbox, `Payload: map[string]string{"id": id}` |
| `internal/modules/products/service/service.go:324` | outbox, `Payload: payload` (caller-supplied `any`) |
| `legacy`, `webhooks`, `tokens`, `analytics` modules | `DeclareMessaging` is a no-op (`_ *messaging.Declarations`) |

**Raw-door call sites in the demo project: 0** (`Publish(`, `PublishToExchange(`,
`PublishMessage{`, `DeclarePublisher(`, `DeclareTypedConsumer`, `NewTypedHandler` all absent
from `*.go` and `*.md`). The demo publishes exclusively through the outbox with struct/map
payloads and never declares a consumer.

## 3. The typed consume side (model to mirror)

| Element | Location | Signature / fact |
|---|---|---|
| `DeclareTypedConsumer[T]` | `messaging/typed_consumer.go:137` | `func DeclareTypedConsumer[T any](decls *Declarations, opts *ConsumerOptions, fn func(context.Context, T) error) *ConsumerDeclaration` — sets `opts.Handler = NewTypedHandler[T](opts.EventType, fn)`; `T` inferred from `fn` |
| `DeclareTypedConsumerWithMeta[T]` | `:181` | same, `fn func(context.Context, T, Metadata) error` |
| `checkTypedConsumerArgs` | `:149` | panics on nil `decls`/`opts` or an `opts` already carrying a `Handler` (declaration-time wiring mistake) |
| `NewTypedHandler[T]` / `NewTypedHandlerWithMeta[T]` | `:96`, `:172` | return `MessageHandler`; decode (JSON) → validate (go-playground tags) → `fn`; failures are `*PayloadError` nacked without requeue |
| `typedHandler[T]` | `:67-71` | `{eventType string; decoder *payloaderr.Decoder[T]; fn}` — immutable after construction, shared across workers and tenants |
| `newTypedHandler` | `:75` | single construction point; `payloaderr.NewDecoder[T](payloaderr.JSONCodec{})` |
| `Metadata` | `:27-52` | `Headers() amqp.Table`, `EventType() string`, `Redelivered() bool` over the delivery |
| `payloaderr.Codec` | `messaging/internal/payloaderr/decode.go:21` | `Unmarshal(data []byte, v any) error` + `Summarize(err, fieldPathIsSchema bool) string` — **decode-only; there is no `Marshal`/encode half** |
| `payloaderr.JSONCodec` | `decode.go:35-48` | the one implementation |
| `payloaderr.Decoder[T]` | `decode.go:50-78` | `Decode(data []byte, dst *T) *Body` |
| Package visibility | `messaging/internal/payloaderr` | `internal/` — not importable by `messaging/streams` (which has its own `typed_consumer.go:139-165`) or by consumers |
| Streams typed consumers | `messaging/streams/typed_consumer.go:139`, `:146`, `:158`, `:165` | `DeclareTypedConsumer[T]`, `WithMeta` (fn gets `*Message`), `DeclareTypedSuperStreamConsumer[T]`, `WithMeta` — return nothing (AMQP lane returns `*ConsumerDeclaration`) |

### Where `PublisherDeclaration` is registered and what `EventType` binds today

| Point | Location | Fact |
|---|---|---|
| `PublisherOptions.EventType` | `messaging/helpers.go:72` | "Event type identifier" — free string |
| `PublisherDeclaration.EventType` | `messaging/registry.go:131` | copied from options by `NewPublisher` (`helpers.go:81`) |
| Registration | `messaging/registry.go:240-275` | logged as `event_type`; duplicate key check (`:256-264`) |
| Replay copy | `messaging/declarations.go:214` | `EventType: p.EventType` when cloning per tenant |
| Validate | `messaging/declarations.go:563` | carried into the validation view |
| Hash | `messaging/declarations.go:651` | `writeString(h, p.EventType)` — part of the declarations fingerprint |
| Consumer uniqueness | `messaging/declarations.go:21-25`, `:251` | `Queue + Consumer tag + EventType` must be unique; `EventType` also feeds `typedHandler.EventType()` and the `PayloadError` |
| Runtime binding | none | Nothing at runtime ties a `PublisherDeclaration` to a `PublishToExchange` call: modules re-spell `Exchange`/`RoutingKey` in `PublishOptions` (see `llms.txt:3220-3227` comment "matches DeclarePublisher in Step 2") |

## 4. Precedent breaks (form to copy)

Read from `.claude/skills/breaking-changes/SKILL.md` and `wiki/migrations.md`.

### 4a. ADR-091 — streams lane opt-in at the build graph (closest: a removed exported surface plus an import gate, same messaging area, and #1305 says `messaging/sealed` is "import-gated on the ADR-091 pattern")

| Artifact | Location | Shape |
|---|---|---|
| ADR | `wiki/adr_091_streams_opt_in_registration.md` | `# ADR-091: …` / `## Context` / `## Decision` (with `### Why not a build tag or a sub-module`, `### Why the seam is not a second Manager`) / `## Consequences` |
| Index | `wiki/architecture_decisions.md:1541` | `### [ADR-091: The Native Streams Lane Is Opt-In at the Build Graph](adr_091_streams_opt_in_registration.md)`; counter at `:2096` reads `through ADR-095` |
| Atom | `wiki/migrations.md:6373` | `### [C61.25] the native streams lane is opt-in at the build graph · compile-break + breaking · when: match` with `detect:` (three `git grep` lines, note that a repo-grep miss is not proof), `scope:`, `gate:` (match / no-match), `apply:`, `verify:` (`go build ./...` clean + `go list -deps` negative check), `ref:` (`gaborage/go-bricks#1169 · [ADR-091](…) · files`) |
| Hop row | `wiki/migrations.md:49` (E61 row) | the row's kind list carries `compile-break + breaking (C61.25 — …)`, the count column, the build-caught column ("only the compile half — …"), and a trailing "and if you …" clause per atom |
| Skill line | `.claude/skills/breaking-changes/SKILL.md:60` | `- **Streams lane is opt-in at the build graph (ADR-091):** … See \`[C61.25]\`.` |

### 4b. ADR-033 — bounded publish retries + outbox dead-lettering (closest on the publish path itself: changed what `Publish`/`PublishToExchange` return and moved an outbox interface)

| Artifact | Location | Shape |
|---|---|---|
| ADR | `wiki/adr_033_outbox_retry_count_status_parking.md` | `# ADR-033: …` / `## Context` (`:59`) / `## Decision` (`:89`) / `## Consequences` (`:121`) |
| Index | `wiki/architecture_decisions.md:426` | standard `### [ADR-033: …](…)` entry; `:464` records the numbering note shared with ADR-034 |
| Atoms | `wiki/migrations.md:590` `[C45.7]` "`messaging.Publish`/`PublishToExchange` are now bounded — return `ErrPublishRetriesExhausted` · silent-behavior · when: match"; `:598` `[C45.8]` new config keys · silent-config · when: no-match | two atoms on one hop: one for the behavior change, one for the config keys |
| Skill lines | `SKILL.md:20` (original) and `:52` (amendment: "Outbox refuses an unpublishable destination (ADR-033 amendment)") | an amended ADR gets a second skill line, not an edit of the first |

### 4c. Other precedents named in the brief, for the record

- **v0.17 worker default** — `SKILL.md:17`: "**Consumer concurrency (v0.17.0):** default workers `1` → `NumCPU * 4`; set `Workers: 1` when ordering matters." No ADR, no atom (pre-dates both conventions).
- **ADR-034 echo-type removal** — **not in the skill index**: `grep ADR-034 .claude/skills/breaking-changes/SKILL.md` returns nothing. It appears only in `wiki/migrations.md` (`:418` gist, `ref:` lines `:459`, `:487`, `:516`, `:542`, `:564`). Citing it as a skill-line precedent would be citing a line that does not exist.

## 5. apidiff reality

Source: `.github/workflows/ci-v2.yml:1011-1198` (job `apidiff`, "API compatibility") and the repo memory notes.

| Rule | Fact | Pointer |
|---|---|---|
| Baseline | head snapshot vs latest GA tag, then the base branch's own incompatible set is subtracted; the gate fails only on the delta this PR introduces | `ci-v2.yml:1011-1027`, `:1149-1160` |
| Escape hatch 1 | PR title matches `^[a-zA-Z]+(\([^)]*\))?!:` | `ci-v2.yml:1176` |
| Escape hatch 2 | PR carries label exactly `breaking-approved` (jq array-element match) | `ci-v2.yml:1183` |
| Event | runs on `pull_request` only; `PR_TITLE`/`PR_LABELS` come from the event payload that started the run. A `pr edit --title` does NOT rerun (no `edited` type); the next push, or close+reopen, does | `ci-v2.yml:1028-1036`; memory `reference_apidiff_title_marker_needs_fresh_event.md` (observed on #1045) |
| Stale base.sha | `base.sha` refreshes only when the merge-base moves; a `synchronize` push on an old root blames main's own breaks on the PR. Fix: rebase onto `origin/main`, never retitle. Filed as #1233 | memory `reference_apidiff_stale_merge_base_phantom_break.md` |
| Interface method removal | `Client.Publish` / `AMQPClient.PublishToExchange` removed from an exported interface → `-incompatible` reports both the method removal and, for any consumer type asserting the interface, a compile break | `apidiff` semantics; the workflow prints the full report at `:1142-1143` |
| Map/slice field | adding a map/slice/func field to a shipped exported struct makes it non-comparable → incompatible. `PublishOptions` already holds `Headers map[string]any` (`messaging.go:37`), so it is already non-comparable; a new `Publisher[T]` type has no old version to diff | memory `reference_apidiff_map_field_breaks_comparability.md` |
| Variadic / arity | adding `...Option` or a parameter to a shipped exported func is incompatible (type identity); `make check` has no apidiff step | memory `reference_apidiff_variadic_incompatible.md` (#640, #1244) |
| Module scope | `GOWORK=off`; `internal/` auto-ignored (so `messaging/internal/payloaderr` changes are invisible to the gate); `tools/migration` excluded | `ci-v2.yml:1013-1015`, `:1083` |
| Stacked PRs | each link's base is the branch below, so each link's delta is judged against that link; every link that removes a symbol needs its own `!` title or label | `ci-v2.yml:1016-1017`; CLAUDE.md stacked-PR rule |
| Local reproduction | `git worktree add --detach /tmp/base <tag>`; `GOWORK=off apidiff -m -w …` on both; `GOWORK=off apidiff -incompatible -m base.snap head.snap` | memory `reference_apidiff_variadic_incompatible.md`; pinned `apidiff@v0.0.0-20260824195058-e88cd73687aa` (`ci-v2.yml:1057`) |

## 6. Facts that did not match the brief's premises

- The brief names `payloaderr.JSONCodec` as if exported. It lives in `messaging/internal/payloaderr`
  and its `Codec` interface is decode-only (`Unmarshal` + `Summarize`); no encode half exists to mirror.
- The brief lists ADR-034 as a skill precedent; the skill index has no ADR-034 line.
- `DeclarePublisher` on the AMQP lane returns a declaration, not a handle; the streams lane's
  `DeclarePublisher` already returns the handle (`*streams.Publisher`) that #1305's
  `Publisher[T]` shape resembles.
- `scheduler.JobContext.Messaging()` is typed `messaging.Client` (`scheduler/job.go:61`), not
  `AMQPClient`, so a job today reaches only the default-exchange `Publish` door.
- `testing/mocks` exports doubles for both raw methods; they are part of the consumer-visible
  surface apidiff will report.
