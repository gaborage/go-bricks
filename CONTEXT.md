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
Turning a database section into the shape a connection can be opened from:
inferring what can be inferred, filling documented defaults, and rejecting
what a driver would silently drop.
_Avoid_: defaulting, validation (on its own), sanitizing, hydration

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

**Delivered-but-empty**:
A database section whose identity keys were delivered but every value is
empty (an unset envsubst variable, an empty secretKeyRef). Misconfiguration,
not absence (ADR-051).
_Avoid_: blank, partial, half-configured
