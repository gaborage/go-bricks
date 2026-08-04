# ADR-046: Cache Readiness Is Strict by Default, with a Visible Opt-Out

**Status:** Accepted
**Date:** 2026-08-04

## Context

Until #860, `GET /ready` ran the cache probe and then discarded its result: the probe
leased an instance, the lease answered from the pool without a network round trip, and the
outcome never reached the response. A service whose cache was dead reported `ready`
indefinitely. #860 fixed the visibility half — the probe now calls `Cache.Health(ctx)` on
the leased instance (one Redis `PING` per poll) and the 200 body carries `cache` and
`cache_stats` — and added `cache.critical` so a deployment could turn that visibility into
a `503`.

`cache.critical` shipped defaulting to `false`. That leaves the original defect intact for
everyone who does not read the release notes: a rate limiter, a session store, or an
idempotency ledger whose Redis is down still answers `/ready` with `200`, stays in the
Service endpoints, and keeps taking traffic it cannot serve correctly. An opt-in fix for a
silent-failure bug protects only the operators who already suspected the problem. The
population that most needs the `503` is the population least likely to go looking for a
new key.

Two properties of the surrounding surface shaped the decision. First, the flag has never
appeared in a tagged release — it and the probe change are both unreleased atoms in the
same v0.56.0 hop (`[C56.9]`, `[C56.10]`), so no fleet has adopted the lenient default and
flipping it now breaks no deployed configuration that was written against it. Second,
`/ready` carries no IP allowlist and no authentication, and the cache probe's error is a
`cache.ConnectionError` rendering the Redis host, port and resolved dial IP. (No tenant
identity: the probe leases the empty top-level key, so `CacheManager.Get`'s `failed to
create cache for key %q` wrap renders `key ""` on the cold path.) Under an opt-in default
that disclosure reached only deployments that
deliberately asked for the `503`. Under a strict default it becomes the shipped behavior
of every cache-enabled service, which makes it this ADR's problem rather than an
inherited one.

## Decision

**Strict by default: an absent `cache.critical` means the cache probe is critical.** A
cache-enabled deployment that says nothing now answers `/ready` with `503` while its cache
is unreachable. The framework's position is that a service which configured a cache
configured it because it needs one, and that the safe reading of silence is "this
dependency matters". Both readings of silence are guesses; this one fails toward removing
a pod from rotation, which is recoverable, rather than toward serving wrong answers from a
pod the load balancer believes is healthy, which is not.

**The escape hatch is kept, not removed — the opt-out *is* the hardening feature.** The
tempting stricter position is to delete `cache.critical` entirely and make cache
criticality non-negotiable. That is weaker, not stronger, because the framework does not
own the probe wiring. An operator who finds `/ready` too strict controls the Kubernetes
manifest, and their cheapest remedy is to repoint `readinessProbe` at `/health` — which
checks no dependency at all and would silence the database probe along with the cache one.
That rewrite lives in a manifest the framework cannot see and cannot warn about — and
where the manifest is kept outside the service repository, as deployment manifests
commonly are, no `git grep` of that repository surfaces it either. A supported
`cache.critical: false` is a *single-line, auditable* declaration in the service's own
config, greppable wherever that config is reviewed, and it degrades exactly one probe
instead of all three.
Banning the opt-out does not eliminate the lenient deployment; it makes the lenient
deployment invisible and broader in blast radius.

**The opt-out is loud.** When the cache is enabled and `cache.critical` is explicitly
`false`, `Builder.CreateHealthProbes` emits one startup WARN
(`warnIfCacheCriticalityOptOut`, `app/app_builder.go`) naming the key, the consequence
(`/ready` keeps answering `200` while the cache is down, so a dead cache still reports
ready) and the remedy (remove the key to restore the strict default). It follows the
precedent set by ADR-038's `CORS_DEV_WILDCARD` warning: a deliberately weakened posture
should be re-announced on every boot, not just in the commit that introduced it, because
the person debugging a stale-cache incident two years later is not the person who set the
flag. It stays a WARN and never a validation error — a lenient cache is a legitimate
choice, and failing startup over it would push operators straight into the invisible
manifest rewrite the previous paragraph exists to avoid. Nothing is emitted under the
strict default (nil), and nothing is emitted when the cache is disabled *and* the default
Redis connector is in use, where `cache.enabled: false` makes the lease report
`not_configured` with a nil error that can never `503`. A custom `Options.CacheConnector`
never consults `cache.enabled`, so its probe is live and critical regardless — the WARN
fires for it too, gated on whether the probe can fail rather than on the config key.

**Pointer tri-state (`*bool`), not `bool`.** `CacheConfig.Critical` is `*bool`, read
through `Config.IsCacheCritical()`, and `"cache.critical"` is deliberately **not**
registered in `loadDefaults`. Both halves are load-bearing. With a strict default, a bare
`bool`'s Go zero value (`false`) means the *opposite* of the shipped default, so a
`config.Config` assembled in Go — the `NewWithConfig` path, which also bypasses
`config.Validate` — would be silently lenient while a byte-equivalent YAML-loaded config
was strict. And registering any koanf default would populate the pointer on every load,
collapsing absent and explicit-`false` into one indistinguishable state and destroying the
signal the startup WARN is gated on. This mirrors `ServerConfig.LogRoutes` /
`Config.ShouldLogRoutes()`, which is a `*bool` with no registered default for the same
reason. It deliberately *diverges* from `ShouldLogRoutes` in one place: `IsCacheCritical`
returns `true` for a nil receiver, where `ShouldLogRoutes` returns `false`. A structural
mirror would return this flag's off value for the most absent config there is, which is
precisely the lenient-by-accident failure mode the pointer exists to prevent; for a
strict-by-default flag, the off value is the one an operator has to ask for.

**The `503` body is sanitized to `cache unavailable`; the full error keeps two other
channels.** The cache probe declares a fixed public string
(`cacheUnavailableMessage`, `app/health.go`) which `readyCheck` prefers over
`result.Err.Error()` via `publicProbeError`. `/ready` is unauthenticated and
allowlist-free, and under a strict default its `503` path is now reached by every
cache-enabled service during any Redis outage, so shipping the connector error verbatim
would make Redis topology disclosure the framework's default behavior rather than an
opt-in consequence. The full error still reaches the application log on every `503`
(`readyCheck` logs it at ERROR with a `component` field before returning) and the
IP-allowlisted `GET /_sys/health-debug` endpoint where that is enabled
(`debug.enabled: true`, `debug.endpoints.health`; the allowlist is itself a pass-through
when `AllowedIPs` is empty, which the framework already warns about separately). The
sanitization is **per-probe, not a blanket rewrite of the shared branch**: the database
and messaging `503` bodies are byte-identical to before, because those probes leave
`PublicErr` empty and `publicProbeError` falls back to the raw error. A single fixed
string, rather than a per-shape classifier, covers every cache failure shape
(`*cache.ConnectionError` — which already wraps the ping timeouts the 500ms cap produces —
`cache.ErrManagerClosed` from a closed manager, and whatever a custom
`Options.CacheConnector` returns) and cannot regress when a new one appears.

**Correlated eviction is an accepted risk, mitigated by
`readinessProbe.failureThreshold` — a Kubernetes primitive the framework deliberately does
not reimplement.** The honest cost of a strict default is that one shared Redis is now a
single point of readiness failure for every replica simultaneously: a blip that used to be
a latency regression can drain the whole Deployment from rotation at once, and a rollout
during a Redis outage stalls. That is a real availability regression for a service where
the cache is a pure optimisation in front of a database that can absorb the miss, and
those services are expected to set `cache.critical: false` and take the WARN. For everyone
else, the mitigation is `failureThreshold` (default 3 in the documented manifest, with
`periodSeconds: 10`): a transient outage has to persist across three consecutive polls
before a pod leaves the endpoints. GoBricks does not add its own consecutive-failure
counter, hysteresis window, or half-open state. The orchestrator already owns that
primitive, owns the poll clock the threshold is expressed in, and applies it uniformly
across every probe on the pod; a second in-process threshold would multiply against the
external one (three framework failures × three kubelet failures = nine polls to react),
be invisible to `kubectl describe`, and drift from the manifest that operators actually
tune. The framework's job is to report the truth on each poll; deciding how many
consecutive truths constitute an outage is the prober's.

## Consequences

**Breaking behavior change, with no compiler signal.** Any deployment upgrading from
v0.55.0 with `cache.enabled: true` starts failing readiness during a Redis outage without
changing a line of config — and the compiler cannot help, because the change is a default
flip in a key that was never set. [wiki/migrations.md](migrations.md) (`[C56.10]`) carries
it as a `silent-behavior · when: no-match` atom with a runnable detect/gate/apply/verify,
and `[C56.11]` covers the sanitized `503` body separately for anyone whose alerting parsed
the old error string. `[C56.10]`'s `detect` is the inverse of the usual one: finding
nothing is the actionable result.

**`CacheConfig.Critical` is now `*bool`.** A Go-assembled `config.Config` setting it needs
`new(true)` / `new(false)`; a keyed composite literal that omits it is unaffected and gets
the strict default, which is the point. YAML and `CACHE_CRITICAL` are unchanged — removing
the koanf default does not affect env binding, since the env provider carries no prefix
filter and does not require a pre-registered key.

**The `503` body's `error` value is no longer the connector error.** Anything parsing
`/ready`'s `503` body for a Redis address — an alert rule, a runbook grep, a synthetic
check — now reads the constant `cache unavailable`. Move that consumer to the application
log or `/_sys/health-debug`. This narrows what the framework discloses; it does not
narrow what an operator can see.

**Scope held deliberately narrow.** The database probe (always critical) and the messaging
probe (never critical, still no knob) are untouched in both criticality and `503` body.
`GET /health` is unchanged and still dependency-free, which is what makes it the wrong
target for a readiness probe and the right one for liveness. No `degraded` status was
introduced — `/ready` answers a binary question for a binary consumer, and a third status
code would be invented vocabulary no orchestrator reads. A failing cache is still not
fatal at startup: the process boots, `Builder.preInitCache` logs a WARN, and the pod simply
never reports Ready, matching the manager contract that a failing cache is disabled rather
than fatal.

**What this does not protect.** The probe leases key `""`, so it observes only the
top-level `cache.*` connection. A deployment whose caches live exclusively under
`multitenant.tenants.<id>.cache` gets nothing from the strict default — top-level
`cache.enabled: false` reports `not_configured` with no error, and `/ready` stays `200`
however many tenant Redis instances are down. A value under
`multitenant.tenants.<id>.cache.critical` parses (the per-tenant schema reuses
`CacheConfig`) and is ignored. Neither setting replaces a Redis-side alert.

See [wiki/cache.md](cache.md#readiness) for the full readiness state table, the
`cache.critical` semantics, and the Kubernetes probe wiring, and
[wiki/migrations.md](migrations.md) (`[C56.10]`, `[C56.11]`) for the upgrade atoms.
