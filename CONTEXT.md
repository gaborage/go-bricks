# GoBricks

Go framework for building production-grade microservices. Single context: the
vocabulary below names framework concepts the code, tests, ADRs and wiki share.

## Language

### Configuration

**Database section**:
One database configuration block, wherever it sits in the tree — the root
`database`, an entry under `databases`, or a tenant's `database`.
_Avoid_: database config, DB block, DSN block

**Placement**:
Where a database section sits in the tree: `root`, `named`, or `tenant`.
Placement decides whether the section may be absent, whether a `manager` block
is allowed, and how its errors are addressed.
_Avoid_: role, kind, level, scope

**Normalization**:
Turning a configuration — the whole loaded tree or any one section — into the
shape it is consumed from: inferring what can be inferred, filling documented
defaults, and rejecting only what cannot be shaped (a contradiction, or a value
a consumer would silently drop). A database section is the canonical example:
after normalization a connection can be opened from it.
_Avoid_: defaulting, validation (on its own), sanitizing, hydration

**Check**:
Rejecting a normalized configuration without changing it: required identity
that is still missing, and rules that span fields or sections. Normalization
and check together are what `Validate` does; a normalized configuration that
passes check is valid.
_Avoid_: validation (for this phase alone), verification, assertion, linting

**Strictness**:
How normalization treats an explicit value that contradicts an inferred one:
`startup` fails fast; `connect` tolerates it and lets the vendor's own error
surface at dial.
_Avoid_: mode, level, policy, static/dynamic

**Verdict**:
The outcome of resolving a database section: `absent` (no identity delivered —
a supported posture, ADR-047), `normalized`, or the untyped-DSN outcome that
only the caller can judge fatal. Absence is a verdict, never an error.
_Avoid_: result, status, outcome (as a noun in code)

**Absence**:
A database section that carries no identity field at all. Anything less than
absence and less than complete is misconfiguration.
_Avoid_: missing, unconfigured, disabled, empty

**Tri-state setting**:
A setting whose absence means "the documented default applies", distinct from
an explicit off and from an explicit value. Encoded as a pointer
(`cache.critical`, `keystore.secretminlength`) so a hand-built configuration
cannot conflate absent with off — the koanf door tells them apart by key
presence (`cache.critical`) or by a registered default equal to the documented
one (`keystore.secretminlength`), the literal door only by nil.
_Avoid_: optional, nullable, flag, opt-out (for the setting itself)

**Delivered-but-empty**:
A database section whose identity keys were delivered but every value is
empty (an unset envsubst variable, an empty secretKeyRef). Misconfiguration,
not absence (ADR-051).
_Avoid_: blank, partial, half-configured

### Application lifecycle

**Slot**:
The framework-side module that owns one resource kind's whole application
lifecycle — probe, pre-init, start, close — so that adding a kind is one slot,
not an edit in every place that enumerates kinds. There is one slot per kind
(database, messaging, cache, streams).
_Avoid_: resource kind (that is what fills a slot), manager wrapper, component

**Probe description**:
What a slot hands readiness so its kind can be judged: a fixed component name,
whether the kind is critical, how to lease it, how to check it is live, and
which of its statistics may appear on the unauthenticated `/ready` body.
_Avoid_: health check, prober (the exported interface), probe config

**Readiness**:
The single judgment of whether the application may take traffic, and the two
views of it — the `/ready` verdict and body, and the debug detail — both
produced from the same probe descriptions and the same list of statuses that
count as ready.
_Avoid_: health (as the noun for this), liveness, ready check

### Messaging

**Delivery pipeline**:
Everything that happens to one consumed message between "bytes arrived" and
"outcome recorded", regardless of lane: trace extraction, span, per-message
lease scope, handler invocation, panic-to-error, duration and count, failure
log. Yields one outcome — succeeded, handler error, or panicked.
_Avoid_: consume loop, message processing, worker (that is the concurrency
shape around the pipeline, not the pipeline)

**Carrier**:
The header source a lane hands the delivery pipeline so trace context can be
read from it — AMQP 0.9.1 headers for the classic lane, AMQP 1.0 application
properties for the streams lane.
_Avoid_: headers (ambiguous across lanes), propagator, accessor (the code shape)

**Settlement**:
The lane-specific step that turns a delivery outcome into a broker action:
ack or nack-without-requeue on the classic lane, commit-offset or skip on the
streams lane. Policy such as "never requeue" and "commit only after success"
lives here, not in the pipeline.
_Avoid_: ack (one lane's word), commit (the other lane's word), completion

**Environment port**:
The seam through which the streams lane reaches the broker — declaring
streams, querying and storing offsets, constructing consumers and producers —
with the vendor environment as its production adapter and an in-memory fake
for tests.
_Avoid_: env, client (ambiguous with the AMQP client), connection

### Tenancy

**Control-plane key**:
The `""` key: the deployment's own resources — the root `database:` and
`messaging:` blocks, or whatever a custom resource source returns for `""`.
Never a tenant; no resolver can produce it.
_Avoid_: root key, empty key, default tenant, shared key

**Tenancy**:
Which key a resource kind is resolved and replayed under when multitenant is
enabled: `per-tenant` (the resolved tenant) or `shared` (the control-plane
key). The ledgers carry one; the messaging kind carries one.
_Avoid_: mode, scope, isolation, tenant model

**Replay**:
Applying validated declarations to one key — declare infrastructure, start
consumers — exactly once per key, idempotent on the declaration hash.
_Avoid_: bootstrap, setup, fan-out (the relay's per-tenant pass)

**Tenant stamp**:
The tenant identity a producer writes into the carrier from its authenticated
context — never copied from a payload. The consumer reads it as
identification, not authorization.
_Avoid_: tenant header, tenant tag, tenant id (for the carried value)

**Partition key**:
The value hashed to choose a super-stream partition; on the ordered lane it is
the tenant stamp, so one tenant's messages keep their order.
_Avoid_: routing key (the classic lane's word), shard key, hash key

**Hold**:
Per-tenant parking that keeps every later message for a tenant behind a failed
one until that one succeeds, so order survives a failure without stalling the
tenants that share its partition.
_Avoid_: DLQ (where a classic-lane message goes instead), quarantine, retry
queue, parking lot

### Observability

**Sink**:
A place a framework-reported value ends up: a log field, a span exception
event, a span status description, an audit event. A value the framework reports
once may reach several sinks, and each sink is judged on its own.
_Avoid_: destination, backend, exporter, output

**Off-platform sink**:
A sink whose retention, access model and export path the operator does not
control — today, span exception events and span status descriptions, which
leave with the tracing exporter. A log field is on-platform: the operator owns
its retention and the sensitive-data filter sees it.
_Avoid_: external, third-party, remote

**Opaque payload**:
Bytes or a string whose structure the sensitive-data filter cannot see into
by field name — a pre-encoded JSON document, a JWK, a PEM block, a response
body logged as one value. Masking one is a matter of parsing it or recognising
its shape, never of naming fields.
_Avoid_: pre-encoded payload (one cause, not the class), raw payload (collides
with `RawMessage` and the raw-SQL doors), blob

### Query building

**Identifier argument**:
A caller-supplied string that becomes SQL _syntax_ — a table, a column, or an
alias. A method may take one beside a bound value, and then only the value is
placeheld: `f.Eq(column, value)` binds the value and writes the identifier into
the statement. Because it becomes syntax, an identifier argument is safe only
where its door validates it against the grammar of its identifier context
first.
_Avoid_: column name, field, parameter, identifier (unqualified)

**Bound value**:
A caller-supplied value that reaches the database as a placeholder and never as
syntax. Naming it apart from an identifier argument is what keeps "this API
parameterizes its values" from being read as "this API is safe" — a method that
does the first for one argument may do neither for the other.
_Avoid_: argument, parameter (on its own), binding

**Identifier context**:
Which grammar an identifier argument is validated against at a given door:
`table` (one optional inline alias), `identifier` (bare or qualified), and
`clause` (plus a bounded direction grammar). The context belongs to the door,
not to the caller, so a door that takes a predicate rather than an identifier
has no identifier context and is a raw-SQL door instead — such a door's call
sites carry the inline `// SECURITY: Manual SQL review completed - <rationale>`
annotation (CLAUDE.md, Security Guidelines). A door whose shape no existing
context describes earns a new one rather than the nearest fit.
_Avoid_: validation mode, identifier type, grammar level

### Testing

**Tripwire guard**:
A test asserting a pinned measurement to catch one specific structural change
(the alloc guards catching a flat-adapter conversion, ADR-026). Its margin
absorbs environment noise and must stay smaller than the signal it exists to
catch; when the environment shifts — a toolchain bump — the pin is re-measured
and re-pinned, never widened.
_Avoid_: perf test, benchmark assertion, alloc test (as the concept)

### Build

**Language floor**:
The `go` directive: the oldest Go a consumer may build the framework with, and
the language version its code is written to. Raising it is a deliberate hop
for every consumer and carries an ADR; a standard-library behavior change
reaches a consumer through THEIR toolchain, never through ours.
_Avoid_: Go version (unqualified), required Go, minimum Go

**Toolchain pin**:
The `toolchain` directive: the exact Go this module's own CI, contributors and
released binaries build with. Invisible to consumers, so a routine dependency
bump may move it — after the suite is green under the new toolchain, and never
by loosening the test that went red.
_Avoid_: Go version (unqualified), build Go, CI Go
