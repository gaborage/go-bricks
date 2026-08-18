# Kill the dead `app/` lifecycle surface — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete the sixteen exported `app/` symbols that no code outside `app/` references (`IsAvailable` exists on both helpers, hence sixteen from the spec's fifteen list entries), unexport the eight debug JSON response types, and record the removal as ADR-067 plus migration atom `[C60.4]` — clearing the ground for the per-kind lifecycle slots that land in PR3.

**Architecture:** `MessagingInitializer` and `ConnectionPreWarmer` are pass-through helpers: each holds a logger plus one or two manager pointers that `App` already holds, and each is constructed by exactly one Builder step and called by exactly one `prepareRuntime` line. Both fold into unexported methods on `App` — the fail-vs-warn consumer grading (#907) becomes `App.prepareRuntimeConsumers`, and the pre-warm pass with its tri-state publisher-readiness outcome becomes `App.preWarmSingleTenant` and friends. The eight debug JSON types are internal response shapes with no consumer use; they become lowercase with their `json:` tags byte-identical, so the wire contract does not move. `Options.Database` and `Options.MessagingClient` are read by nothing at all and simply go.

**Tech Stack:** Go 1.26, testify (`assert`/`require`), zerolog, `golangci-lint` v2 via `make check`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-16-app-readiness-and-lifecycle-slots-design.md` — "Lifecycle slots (card 3)": decision 6 is the exact kill list and LEAVE list this PR executes; decisions 1–5 and 7 are what ADR-067 records. Vocabulary: `CONTEXT.md` ("Slot"). Preceding ADR: `wiki/adr_066_readiness_one_module.md` (PR1a/PR1b — **do not touch**).

## Global Constraints

Copied verbatim from the controller's brief and CLAUDE.md. Every task's requirements implicitly include this section.

- **Branch:** work on `feature/app-kill-dead-lifecycle-surface` (stacked on `feature/app-readiness-one-body-one-gate`). Never switch branches. Never push to `main`.
- **Behavior is identical except for the removed exported names.** After this PR `prepareRuntime` still:
  1. **prepares AMQP consumers with the #907 fail-vs-warn grading** — `Manager.EnsureConsumers(ctx, "", decls)` runs unconditionally (it is what declares exchanges, queues and bindings); on failure, a declaration set containing **at least one consumer** (`len(decls.Consumers()) > 0`) returns `fmt.Errorf("failed to start single-tenant consumers: %w", err)` and aborts startup, while an empty consumer set — including a service with no messaging configured at all — logs `WARN "Failed to start single-tenant consumers"` and continues; success logs `INFO "Single-tenant consumers started successfully"`;
  2. **skips consumer bootstrap at startup in multi-tenant mode** — `cfg.Multitenant.Enabled` returns early with `INFO "Multi-tenant mode: consumers will be started per tenant on demand"` and never calls `EnsureConsumers`;
  3. **pre-warms database and then messaging in single-tenant mode**, in that order, with the same readiness poll (`client.IsReady()` every 100 ms) and the same tri-state outcome — ready (`INFO "Pre-warmed messaging publisher"`), not ready in time (`WARN "Messaging publisher not ready within pre-warm window; continuing startup"`, never fatal), caller canceled (propagates `fmt.Errorf("publisher readiness wait canceled: %w", ctx.Err())`) — and any accumulated pre-warm error surfaces only as `WARN "Pre-warming completed with warnings"`, never as a startup failure;
  4. runs those three in the same order as today, with the pre-warm pass gated on single-tenant mode **and** at least one of `dbManager` / `messagingManager` being non-nil.
- **The kill list is exactly spec decision 6's.** Delete: `MessagingInitializer`, `NewMessagingInitializer`, `CollectDeclarations`, `SetupLazyConsumerInit`, `IsAvailable` (both), `LogDeploymentMode`, `PrepareRuntimeConsumers`, `ConnectionPreWarmer`, `NewConnectionPreWarmer`, `PreWarmSingleTenant`, `PreWarmDatabase`, `PreWarmMessaging`, `LogAvailability`, `Options.Database`, `Options.MessagingClient`. Unexport (JSON unchanged): `HealthDebugInfo`, `ComponentHealth`, `HealthSummary`, `DebugResponse`, `GCInfo`, `GoroutineInfo`, `GoroutineStack`, `PotentialLeak`.
- **The LEAVE list is respected.** Do not touch: the `Builder` steps (`CreateApp`, `InitializeRegistry`, `ConfigureRuntimeHelpers`, `CreateHealthProbes`, `RegisterClosers`, `RegisterReadyHandler`) or their names; card-2 types (`ManagerConfigBuilder`, `ResourceManagerFactory`, `FactoryResolver`, `MessagingClientFactoryOptions`, `LogFactoryInfo` and `TestResourceManagerFactoryLogFactoryInfo`); `ResourceProvider`, `SingleTenantResourceProvider`, `MultiTenantResourceProvider` and their `SetDeclarations`; `app/module_metadata.go` (external `go-bricks-openapi` consumer); `SignalHandler`, `TimeoutProvider`, `OSSignalHandler`, `StandardTimeoutProvider`; `IPWhitelist`/`NewIPWhitelist` (ADR-049 surface); `Info` (not on the kill list); `app/streams_setup.go`.
- **No exported API additions.** Everything this PR introduces is unexported.
- **Commit type is `refactor(app)!:`** — the `!` is load-bearing twice: the CI `apidiff` job fails a new INCOMPATIBLE change **unless** the squash PR title carries a conventional-commit `!` marker, and release-please derives the version bump from that same title.
- **Documentation is mandatory and lands in this PR:** ADR-067 file + `wiki/architecture_decisions.md` index entry + the `ADR-001 through ADR-066` → `ADR-067` counter bump, atom `[C60.4]` in `wiki/migrations.md` under hop `## E60`, one line in CLAUDE.md's `## Breaking Changes`, and the two wiki wording fixes (`wiki/startup_defaults.md`, `wiki/migrations.md`'s `[C57.8]`).
- **`wiki/adr_066_readiness_one_module.md` is untouched.**
- **Test names:** camelCase for test function names (`TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode`, never `Test_Prepare_Runtime_Consumers`). Table-driven case names use snake_case (`{name: "positive_config_wins"}`).
- **Tests: replace, don't layer.** `TestCollectDeclarations` (its function is dead) and `TestSetupLazyConsumerInit*` / `TestSetupMultiTenantLazyInit` are deleted outright; `TestPreWarmDatabase` / `TestPreWarmMessaging` fold into App-level single-tenant pre-warm tests; the fail-vs-warn grading and the tri-state readiness outcome each keep a test at their new unexported home; `newTestAppFixture` stops constructing the two helpers.
- **Commits:** `git commit -F <file>` with a message file — the commit hook blocks heredoc `-m`. **Never** pass `--no-gpg-sign`; if signing fails because 1Password is locked, stop and report it.
- **Every commit step in Tasks 1–4 is preceded by two checks:** `git branch --show-current` must print `feature/app-kill-dead-lifecycle-surface`, and `make check` must pass on the tree about to be committed. Implementers also run the targeted `go test` commands named in their task, plus `gofmt -w` on the files they touched (the code blocks below are written for readability, not for gofmt's alignment). **Implementers do not run `make mutate` or push** — the controller runs the full gate set (Task 5) before the push.
- **No `//nolint`.** If a linter fires, fix the code.

## Reference: verified reference counts

Every symbol below was grepped across the whole repo before this plan was written.

| Symbol | References outside `app/` (code) | Doc mentions |
| --- | --- | --- |
| `MessagingInitializer`, `NewMessagingInitializer` | **none** | `wiki/migrations.md` `[C57.8]` scope line |
| `CollectDeclarations`, `SetupLazyConsumerInit`, `LogDeploymentMode` | **none** | none |
| `PrepareRuntimeConsumers` | **none** | `wiki/migrations.md` `[C57.8]` scope + ref lines |
| `ConnectionPreWarmer`, `NewConnectionPreWarmer`, `PreWarmSingleTenant`, `PreWarmDatabase`, `PreWarmMessaging`, `LogAvailability`, `IsAvailable` (both) | **none** | `wiki/startup_defaults.md:82` (`ConnectionPreWarmer.awaitPublisherReady`) |
| `Options.Database`, `Options.MessagingClient` | **none read anywhere**; one literal `&Options{Database: nil}` at `app/app_builder_test.go:212` | none |
| the eight debug JSON types | **none** | none |

`wiki/adr_047_database_absence_vs_misconfiguration.md:120`, `wiki/adr_041_shared_ledger_tenancy.md:277` and `wiki/outbox.md:334` mention `app/prewarm.go` (a file path that survives) or the phrase "connection pre-warmer" in prose (not a symbol). They need no edit.

## File Structure

| File | Change | Responsibility after this PR |
| --- | --- | --- |
| `app/messaging_setup.go` | rewrite (130 → ~40 lines) | One unexported method: `App.prepareRuntimeConsumers` — the multi-tenant skip and the #907 fail-vs-warn grading. |
| `app/prewarm.go` | rewrite (252 → ~175 lines) | Unexported `App` methods: `preWarmSingleTenant`, the two per-kind attempts, the two lease helpers, the readiness budget resolver, and `awaitPublisherReady` with its tri-state outcome. |
| `app/lifecycle.go` | modify | `prepareRuntime` calls the two new methods directly; `prepareMessagingConsumers` deletes (it was a pass-through). |
| `app/app.go` | modify | `App.messagingInitializer` and `App.connectionPreWarmer` fields delete. |
| `app/app_builder.go` | modify | `ConfigureRuntimeHelpers` stops constructing the two helpers and stops poking `readinessTimeout`. |
| `app/options.go` | modify | `Options.Database` and `Options.MessagingClient` delete. |
| `app/debug_handlers.go`, `app/debug_health.go`, `app/debug_goroutines.go`, `app/readiness_render.go` | modify | The eight response types become unexported; `json:` tags unchanged. |
| `app/messaging_setup_test.go`, `app/prewarm_test.go`, `app/lifecycle_test.go`, `app/app_test.go`, `app/app_builder_test.go`, `app/debug_handlers_test.go`, `app/debug_goroutines_test.go`, `app/debug_health_test.go`, `app/readiness_render_test.go` | modify | Retargeted at the unexported homes; the dead-function tests deleted; one new JSON wire-shape pin. |
| `wiki/adr_067_lifecycle_slots.md` | **create** | ADR-067. |
| `wiki/architecture_decisions.md`, `wiki/migrations.md`, `wiki/startup_defaults.md`, `CLAUDE.md` | modify | Index entry + counter, atom `[C60.4]` + hop row, wording fixes, one Breaking Changes line. |

**Three decisions worth stating up front, because they go slightly beyond a literal rename:**

1. **`prepareMessagingConsumers` collapses into `prepareRuntimeConsumers`.** Today `prepareRuntime` → `prepareMessagingConsumers` (guards `messagingInitializer == nil || !IsAvailable() || decls == nil`) → `PrepareRuntimeConsumers` (guards `manager == nil` and returns an error). With the exported method gone, the outer guard makes the inner one unreachable, so the two become one function with one guard. The only lost behavior is the `errors.New("messaging manager not configured")` return, which was observable **only** by calling the exported method directly — which is precisely what this PR removes.
2. **`SetupLazyConsumerInit` is deleted, not moved — it is already redundant.** `App.buildMessagingDeclarations` (`app/app.go:117-138`) runs *first* in `prepareRuntime` and already does `if setter, ok := a.resourceProvider.(declarationSetter); ok { setter.SetDeclarations(decls) }`, which sets exactly the `provider.declarations` field that `setupSingleTenantLazyInit` / `setupMultiTenantLazyInit` set. Both `SingleTenantResourceProvider` and `MultiTenantResourceProvider` implement `SetDeclarations` (`app/resource_provider.go:143,265`). The `declarationSetter` path is a superset: it also covers a provider that is neither concrete type, where `SetupLazyConsumerInit` only logged a WARN.
3. **The pre-warm readiness budget is read from config instead of poked into a field.** `ConfigureRuntimeHelpers` set `connectionPreWarmer.readinessTimeout = b.cfg.Messaging.Reconnect.ReadyTimeout`, where `b.cfg` is the very `*config.Config` the App holds. `App.publisherReadinessTimeout()` reads `a.cfg.Messaging.Reconnect.ReadyTimeout` directly — the same value on the production path, and the "shipped signature must stay byte-identical" comment that justified the poke dies with the constructor. The operator-key pin moves from a Builder test to a direct test on the resolver.

---

## Task 1: Fold `MessagingInitializer` into `App.prepareRuntimeConsumers`

**Files:**

- Rewrite: `app/messaging_setup.go`
- Modify: `app/lifecycle.go:69-92` (`prepareRuntime`), `app/lifecycle.go:161-178` (delete `prepareMessagingConsumers`)
- Modify: `app/app.go:83-86` (drop the `messagingInitializer` field)
- Modify: `app/app_builder.go:250` (drop the constructor call)
- Modify: `app/app_test.go:478-482` (fixture), `app/lifecycle_test.go:535`
- Test: `app/messaging_setup_test.go`

**Interfaces:**

- Produces: `func (a *App) prepareRuntimeConsumers(ctx context.Context, decls *messaging.Declarations) error` — the only consumer-bootstrap entry point. Reads `a.messagingManager`, `a.cfg.Multitenant.Enabled`, `a.logger`. Task 2 does not touch it; Task 4 names it in the docs.
- Consumes: nothing from other tasks.

- [ ] **Step 1: Rewrite the grading tests against the new home**

Replace the whole of `app/messaging_setup_test.go` with the file below. Compared with today it deletes `TestCollectDeclarations`, `TestSetupMultiTenantLazyInit`, `TestSetupLazyConsumerInit`, `TestIsAvailable`, `TestLogDeploymentMode`, `TestNewMessagingInitializer` and the `unknownResourceProvider` stub (all of which exercise functions this task removes), keeps `simpleTestModule` (used by `app/app_test.go:1663`), and retargets every grading test at `App.prepareRuntimeConsumers`.

```go
package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

// Test helper modules
type simpleTestModule struct{}

func (m *simpleTestModule) Name() string             { return "simple-test-module" }
func (m *simpleTestModule) Init(_ *ModuleDeps) error { return nil }
func (m *simpleTestModule) Shutdown() error          { return nil }

// newConsumerBootstrapApp builds the minimal App prepareRuntimeConsumers reads:
// the messaging manager it grades, the deployment mode it branches on, and a logger.
func newConsumerBootstrapApp(log logger.Logger, manager *messaging.Manager, multiTenant bool) *App {
	return &App{
		logger:           log,
		messagingManager: manager,
		cfg:              &config.Config{Multitenant: config.MultitenantConfig{Enabled: multiTenant}},
	}
}

// errBrokerLookupFailed stands in for the broker-config and broker-availability
// failures that make single-tenant consumer bootstrap fail at startup.
var errBrokerLookupFailed = errors.New("broker lookup failed")

// failingBrokerURLProvider fails every broker-URL resolution and counts the
// attempts, so a test can prove consumer bootstrap was reached — or never was.
type failingBrokerURLProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *failingBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return "", errBrokerLookupFailed
}

func (p *failingBrokerURLProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// newFailingConsumerManager wires a *messaging.Manager whose consumer bootstrap
// always fails at broker-URL resolution, before any AMQP client is created.
func newFailingConsumerManager(t *testing.T, log logger.Logger, source messaging.BrokerURLProvider) *messaging.Manager {
	t.Helper()
	return messaging.NewMessagingManager(source, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient {
			t.Errorf("client factory must not run when broker URL resolution fails")
			return nil
		})
}

// noopMessageHandler is a real (non-documentation-only) consumer handler, so the
// fixture below models a service that actually consumes.
type noopMessageHandler struct{}

func (noopMessageHandler) Handle(context.Context, *amqp.Delivery) error { return nil }
func (noopMessageHandler) EventType() string                            { return "order.created" }

// declarationsWithConsumer builds the declaration set of a service that actually
// consumes — the only population whose failed bootstrap aborts startup.
func declarationsWithConsumer() *messaging.Declarations {
	decls := messaging.NewDeclarations()
	decls.RegisterConsumer(&messaging.ConsumerDeclaration{
		Queue:     "orders.queue",
		Consumer:  "orders-consumer",
		EventType: "order.created",
		Handler:   noopMessageHandler{},
	})
	return decls
}

// TestPrepareRuntimeConsumersFailsStartupOnEnsureError pins the fail-fast
// contract: a single-tenant service that declared consumers and cannot start
// them must abort startup rather than boot deaf, serving HTTP while consuming
// nothing.
func TestPrepareRuntimeConsumersFailsStartupOnEnsureError(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	a := newConsumerBootstrapApp(log, newFailingConsumerManager(t, log, source), false)

	err := a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer())

	require.Error(t, err)
	assert.ErrorIs(t, err, errBrokerLookupFailed)
	assert.ErrorContains(t, err, "failed to start single-tenant consumers")
	assert.Equal(t, 1, source.callCount(), "the error must come from consumer bootstrap")
}

// TestPrepareRuntimeConsumersWarnsOnlyWithoutConsumers pins the gate on the
// fatal path. A service that declared no consumers — including every service
// with no messaging configured at all, which reaches this call with an empty
// declaration set and an unresolvable broker URL — must still boot.
func TestPrepareRuntimeConsumersWarnsOnlyWithoutConsumers(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	a := newConsumerBootstrapApp(log, newFailingConsumerManager(t, log, source), false)

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
	assert.Equal(t, 1, source.callCount(), "topology setup must still be attempted")
}

// TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode guards the other
// direction: multi-tenant consumers start lazily per tenant, so a broker that
// cannot be resolved at startup must not abort the boot.
func TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	a := newConsumerBootstrapApp(log, newFailingConsumerManager(t, log, source), true)

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
	assert.Zero(t, source.callCount(), "multi-tenant mode must not start consumers at startup")
}

// TestPrepareRuntimeConsumersSucceedsSingleTenant proves the fail-fast return
// is scoped to real failures: a reachable broker still boots green.
func TestPrepareRuntimeConsumersSucceedsSingleTenant(t *testing.T) {
	log := logger.New("debug", true)
	client := testmocks.NewMockAMQPClient()
	client.ExpectClose(nil)
	manager := messaging.NewMessagingManager(
		&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient { return client })
	defer func() { _ = manager.Close() }()

	a := newConsumerBootstrapApp(log, manager, false)

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
}

// TestPrepareRuntimeConsumersNoOpsWithoutManagerOrDeclarations pins the single
// guard that replaced the old two-layer one: nothing is attempted, and nothing
// fails, when there is no messaging manager or nothing to replay.
func TestPrepareRuntimeConsumersNoOpsWithoutManagerOrDeclarations(t *testing.T) {
	log := logger.New("debug", true)

	t.Run("nil_manager", func(t *testing.T) {
		a := newConsumerBootstrapApp(log, nil, false)
		require.NoError(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	})

	t.Run("nil_declarations", func(t *testing.T) {
		source := &failingBrokerURLProvider{}
		a := newConsumerBootstrapApp(log, newFailingConsumerManager(t, log, source), false)
		require.NoError(t, a.prepareRuntimeConsumers(context.Background(), nil))
		assert.Zero(t, source.callCount(), "no declarations means nothing to replay")
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && go test ./app/ -run TestPrepareRuntimeConsumers -race`

Expected: FAIL to build, with

```text
app/messaging_setup_test.go:106:12: a.prepareRuntimeConsumers undefined (type *App has no field or method prepareRuntimeConsumers)
```

(plus repeats for the other call sites, and `errBrokerLookupFailed redeclared` style errors if any old declaration survived the replacement — if you see those, the file was appended to rather than replaced).

- [ ] **Step 3: Replace `app/messaging_setup.go` with the single unexported method**

Full new contents of `app/messaging_setup.go`:

```go
package app

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/messaging"
)

// prepareRuntimeConsumers starts AMQP consumers according to the deployment mode.
// No-op when no messaging manager was built or no declarations were collected.
//
// Multi-tenant: consumers start per tenant on demand, so nothing runs at startup.
//
// Single-tenant: EnsureConsumers also declares the exchanges, queues, and bindings
// publishers rely on, so it runs regardless; only the failure is graded. A service that
// declared consumers and cannot start them would serve HTTP while consuming nothing, so
// it fails fast. One that declared none — including a service with no messaging
// configured at all — keeps the historical warn-and-continue.
func (a *App) prepareRuntimeConsumers(ctx context.Context, decls *messaging.Declarations) error {
	if a.messagingManager == nil || decls == nil {
		return nil
	}

	if a.cfg.Multitenant.Enabled {
		a.logger.Info().Msg("Multi-tenant mode: consumers will be started per tenant on demand")
		return nil
	}

	if err := a.messagingManager.EnsureConsumers(ctx, "", decls); err != nil {
		if len(decls.Consumers()) > 0 {
			return fmt.Errorf("failed to start single-tenant consumers: %w", err)
		}
		a.logger.Warn().Err(err).Msg("Failed to start single-tenant consumers")
		return nil
	}

	a.logger.Info().Msg("Single-tenant consumers started successfully")
	return nil
}
```

- [ ] **Step 4: Rewire the four call sites so the package compiles again**

**`app/lifecycle.go`** — in `prepareRuntime`, replace

```go
	if err := a.prepareMessagingConsumers(decls); err != nil {
		return err
	}
```

with

```go
	// context.Background(), not ctx: consumers outlive startup, and this call site has
	// never inherited the startup context (unchanged by the helper fold).
	if err := a.prepareRuntimeConsumers(context.Background(), decls); err != nil {
		return err
	}
```

and delete the whole `prepareMessagingConsumers` function (`app/lifecycle.go:161-178`, comment included).

**`app/app.go`** — replace the field block

```go
	// Messaging declarations/initializer, plus the connection pre-warmer (database + messaging)
	messagingDeclarations *messaging.Declarations
	messagingInitializer  *MessagingInitializer
	connectionPreWarmer   *ConnectionPreWarmer
```

with

```go
	// Messaging declarations, plus the connection pre-warmer (database + messaging)
	messagingDeclarations *messaging.Declarations
	connectionPreWarmer   *ConnectionPreWarmer
```

**`app/app_builder.go`** — in `ConfigureRuntimeHelpers`, delete the line

```go
	b.app.messagingInitializer = NewMessagingInitializer(b.logger, b.app.messagingManager, b.cfg.Multitenant.Enabled)
```

**`app/app_test.go`** — in `newTestAppFixture`, delete these five lines from the `&App{…}` literal (the trailing `connectionPreWarmer:` line stays until Task 2):

```go
		messagingInitializer: NewMessagingInitializer(
			log,
			messagingManager,
			cfg.Multitenant.Enabled,
		),
```

**`app/lifecycle_test.go:535`** — in `TestPrepareRuntimeSucceedsWithNoMessagingConfigured`, replace

```go
	require.True(t, a.messagingInitializer.IsAvailable(),
		"a messaging manager is built even with no broker configured — the premise of this guard")
```

with

```go
	require.NotNil(t, a.messagingManager,
		"a messaging manager is built even with no broker configured — the premise of this guard")
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && gofmt -w app/ && go test ./app/ -race`

Expected: `ok  	github.com/gaborage/go-bricks/app	<duration>` — the whole package, not just the new tests, because Step 4 touched shared fixtures.

- [ ] **Step 6: Prove the old surface is gone**

Run:

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks && git grep -nE '(MessagingInitializer|CollectDeclarations|SetupLazyConsumerInit|LogDeploymentMode|PrepareRuntimeConsumers|setupSingleTenantLazyInit|setupMultiTenantLazyInit|prepareMessagingConsumers)' -- '*.go'
```

Expected: no output (exit status 1).

- [ ] **Step 7: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
cat > /tmp/msg-task1.txt <<'MSG'
refactor(app)!: fold MessagingInitializer into App.prepareRuntimeConsumers

MessagingInitializer held a logger and a manager pointer App already holds, was
constructed by exactly one Builder step, and was called from exactly one
prepareRuntime line. It becomes one unexported method.

The #907 fail-vs-warn grading is preserved verbatim: EnsureConsumers runs
unconditionally (it declares the topology publishers rely on), a failure with at
least one declared consumer aborts startup, an empty consumer set warns and
continues, and multi-tenant mode never attempts bootstrap at startup.

CollectDeclarations was dead — App.buildMessagingDeclarations does that work.
SetupLazyConsumerInit was redundant: buildMessagingDeclarations already pushes the
declaration set into the resource provider through the declarationSetter seam, which
covers both concrete providers and any third. prepareMessagingConsumers was a
pass-through whose guard made PrepareRuntimeConsumers' own nil check unreachable, so
the two collapse into one function with one guard; the unreachable
"messaging manager not configured" error goes with the exported method.

BREAKING CHANGE: app.MessagingInitializer, app.NewMessagingInitializer and its
CollectDeclarations, SetupLazyConsumerInit, PrepareRuntimeConsumers, IsAvailable and
LogDeploymentMode methods are removed. Nothing outside app/ referenced them.
MSG
git add app/messaging_setup.go app/messaging_setup_test.go app/lifecycle.go app/lifecycle_test.go app/app.go app/app_builder.go app/app_test.go
git commit -F /tmp/msg-task1.txt
git log -1 --format='%h %G? %s'
```

Expected: the commit lands with a good signature (`%G?` prints `G` or `U`, never `N`). If `git log -1` shows an unexpected subject, the hook rewrote the message — re-check before continuing.

---

## Task 2: Fold `ConnectionPreWarmer` into unexported `App` pre-warm methods

**Files:**

- Rewrite: `app/prewarm.go`
- Modify: `app/lifecycle.go:87-92` (the pre-warm gate in `prepareRuntime`)
- Modify: `app/app.go` (drop the `connectionPreWarmer` field)
- Modify: `app/app_builder.go:250-255` (drop the constructor call and the `readinessTimeout` poke)
- Modify: `app/lifecycle_test.go:322,382`, `app/app_test.go` (fixture), `app/app_builder_test.go:538-548`
- Test: `app/prewarm_test.go`

**Interfaces:**

- Consumes: nothing from Task 1 (the two folds are independent; Task 1 only removed a neighbouring field from the same struct literals).
- Produces:
  - `func (a *App) preWarmSingleTenant(ctx context.Context, decls *messaging.Declarations) error` — the single entry point `prepareRuntime` calls.
  - `func (a *App) attemptDatabasePreWarm(ctx context.Context, errs []error) []error`
  - `func (a *App) attemptMessagingPreWarm(ctx context.Context, decls *messaging.Declarations, errs []error) []error`
  - `func (a *App) preWarmDatabase(ctx context.Context) error`
  - `func (a *App) preWarmMessaging(ctx context.Context, decls *messaging.Declarations) error`
  - `func (a *App) publisherReadinessTimeout() time.Duration`
  - `func (a *App) awaitPublisherReady(ctx context.Context, client messaging.AMQPClient) preWarmReadyOutcome`
  - unchanged: `type preWarmReadyOutcome int`, constants `preWarmReady`, `preWarmNotReadyInTime`, `preWarmCanceled`, `defaultPreWarmReadinessTimeout`, `preWarmReadinessPollInterval`.

- [ ] **Step 1: Rewrite the pre-warm tests against the new home**

Replace the whole of `app/prewarm_test.go` with the file below. Compared with today it deletes `TestNewConnectionPreWarmer`, `TestPrewarmerIsAvailable`, `TestLogAvailability`, `TestPreWarmDatabase` and `TestPreWarmMessaging` (all of which test constructors, availability reporting, or nil-manager guards this task removes) and folds the three publisher-readiness pins into App-level `preWarmSingleTenant` drives. `fakeBrokerURLProvider` stays here — `app/lifecycle_test.go` and `app/messaging_setup_test.go` both use it.

```go
package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

// fakeBrokerURLProvider is a minimal messaging.BrokerURLProvider for tests
// that need a real *messaging.Manager without a real broker.
type fakeBrokerURLProvider struct{ url string }

func (f *fakeBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	return f.url, nil
}

// newPrewarmMockClient returns a MockAMQPClient in the not-ready state with a
// Close expectation, ready for readiness-wait tests (NewMockAMQPClient defaults
// to ready; Manager.Close() closes cached publisher clients).
func newPrewarmMockClient() *testmocks.MockAMQPClient {
	client := testmocks.NewMockAMQPClient()
	client.SetReady(false)
	client.ExpectClose(nil)
	return client
}

// newPrewarmTestManager wires a mock-backed *messaging.Manager for pre-warm tests.
func newPrewarmTestManager(log logger.Logger, client *testmocks.MockAMQPClient) *messaging.Manager {
	factory := func(string, logger.Logger) messaging.AMQPClient { return client }
	return messaging.NewMessagingManager(&fakeBrokerURLProvider{url: "amqp://localhost"}, log,
		messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: time.Hour}, factory)
}

// newPreWarmApp builds the minimal App the pre-warm pass reads: a logger, the
// messaging manager under test, and the config its readiness budget comes from.
func newPreWarmApp(log logger.Logger, manager *messaging.Manager, readyTimeout time.Duration) *App {
	return &App{
		logger:           log,
		messagingManager: manager,
		cfg: &config.Config{
			Messaging: config.MessagingConfig{
				Reconnect: config.ReconnectConfig{ReadyTimeout: readyTimeout},
			},
		},
	}
}

// TestPreWarmSingleTenantSkipsAbsentManagers pins the guard that used to live in
// ConnectionPreWarmer.IsAvailable plus two nil checks: with neither manager built,
// pre-warming is a silent no-op and never reports a problem.
func TestPreWarmSingleTenantSkipsAbsentManagers(t *testing.T) {
	a := &App{logger: logger.New("debug", true), cfg: &config.Config{}}

	require.NoError(t, a.preWarmSingleTenant(context.Background(), messaging.NewDeclarations()))
	require.NoError(t, a.preWarmSingleTenant(context.Background(), nil))
}

func TestAppAwaitPublisherReady(t *testing.T) {
	log := logger.New("debug", true)
	a := newPreWarmApp(log, nil, 0)

	t.Run("already_ready_returns_immediately", func(t *testing.T) {
		client := testmocks.NewMockAMQPClient() // defaults to ready
		assert.Equal(t, preWarmReady, a.awaitPublisherReady(context.Background(), client))
	})

	t.Run("becomes_ready_during_poll", func(t *testing.T) {
		client := newPrewarmMockClient()
		go func() {
			time.Sleep(150 * time.Millisecond)
			client.SetReady(true)
		}()
		assert.Equal(t, preWarmReady, a.awaitPublisherReady(context.Background(), client))
	})

	t.Run("ctx_cancellation_reported_distinctly", func(t *testing.T) {
		client := newPrewarmMockClient()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		outcome := a.awaitPublisherReady(ctx, client)
		elapsed := time.Since(start)

		assert.Equal(t, preWarmCanceled, outcome)
		assert.Less(t, elapsed, time.Second, "must return once ctx expires, not wait out the readiness budget")
	})

	t.Run("configured_budget_elapses_without_readiness", func(t *testing.T) {
		short := newPreWarmApp(log, nil, 150*time.Millisecond)
		client := newPrewarmMockClient()

		start := time.Now()
		outcome := short.awaitPublisherReady(context.Background(), client)
		elapsed := time.Since(start)

		assert.Equal(t, preWarmNotReadyInTime, outcome)
		assert.Less(t, elapsed, time.Second, "must honor the configured budget, not the 5s fallback")
	})
}

// TestAppPublisherReadinessTimeout pins where the pre-warm budget comes from. This is
// the operator-key pin that used to sit on Builder.ConfigureRuntimeHelpers: the value
// is messaging.reconnect.readytimeout, read straight off the App's config.
func TestAppPublisherReadinessTimeout(t *testing.T) {
	tests := []struct {
		cfg  *config.Config
		name string
		want time.Duration
	}{
		{name: "nil_config_falls_back_to_default", cfg: nil, want: defaultPreWarmReadinessTimeout},
		{name: "unset_key_falls_back_to_default", cfg: &config.Config{}, want: defaultPreWarmReadinessTimeout},
		{
			name: "operator_value_wins",
			cfg: &config.Config{Messaging: config.MessagingConfig{
				Reconnect: config.ReconnectConfig{ReadyTimeout: 20 * time.Second},
			}},
			want: 20 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{cfg: tt.cfg}
			assert.Equal(t, tt.want, a.publisherReadinessTimeout())
		})
	}
}

func TestPreWarmSingleTenantAwaitsPublisherReadiness(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient()
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	a := newPreWarmApp(log, manager, 0)

	go func() {
		time.Sleep(150 * time.Millisecond)
		client.SetReady(true)
	}()

	start := time.Now()
	err := a.preWarmSingleTenant(context.Background(), nil)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.Less(t, elapsed, defaultPreWarmReadinessTimeout,
		"must return once the client reports ready, not wait out the full budget")
}

func TestPreWarmSingleTenantContinuesWhenPublisherNeverReady(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	// A short operator budget (messaging.reconnect.readytimeout) so the genuine
	// timeout branch fires without waiting out the 5s fallback.
	a := newPreWarmApp(log, manager, 200*time.Millisecond)

	start := time.Now()
	err := a.preWarmSingleTenant(context.Background(), nil)
	elapsed := time.Since(start)

	// Not-ready-in-time is a WARN, not a startup failure — pre-warm must not
	// propagate an error; PublishToExchange's own readytimeout pre-flight will
	// still absorb a slow first publish later.
	assert.NoError(t, err)
	assert.Less(t, elapsed, time.Second, "must return once the configured budget elapses, not the 5s fallback")
}

func TestPreWarmSingleTenantPropagatesContextCancellation(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	a := newPreWarmApp(log, manager, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := a.preWarmSingleTenant(ctx, nil)
	elapsed := time.Since(start)

	// Cancellation means shutdown/startup abort, not a broker-readiness problem —
	// it propagates instead of being mislabeled by the generic not-ready WARN.
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, time.Second, "must return once ctx expires")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && go test ./app/ -run 'TestPreWarm|TestAppAwaitPublisherReady|TestAppPublisherReadinessTimeout' -race`

Expected: FAIL to build, with

```text
app/prewarm_test.go:63:16: a.preWarmSingleTenant undefined (type *App has no field or method preWarmSingleTenant)
app/prewarm_test.go:73:25: a.awaitPublisherReady undefined (type *App has no field or method awaitPublisherReady)
app/prewarm_test.go:118:22: a.publisherReadinessTimeout undefined (type *App has no field or method publisherReadinessTimeout)
```

- [ ] **Step 3: Replace `app/prewarm.go` with the `App`-method form**

Full new contents of `app/prewarm.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
)

// defaultPreWarmReadinessTimeout is the fallback readiness budget when
// messaging.reconnect.readytimeout carries no positive value (a directly
// constructed App in tests, or a Builder assembled without WithConfig). Mirrors
// config's defaultReadyTimeout (see config/validation.go) — pre-warm and the
// first real publish converge on the same "how long is reasonable to wait for a
// cold client" budget.
const defaultPreWarmReadinessTimeout = 5 * time.Second

// preWarmReadinessPollInterval mirrors messaging's unexported
// readinessCheckInterval (see messaging/constants.go) so both readiness-wait
// call sites share one poll cadence, without exporting an internal messaging
// constant just for this.
const preWarmReadinessPollInterval = 100 * time.Millisecond

// preWarmSingleTenant pre-warms connections for single-tenant deployments.
// It establishes database connections and messaging consumers/publishers upfront.
// Errors are logged as warnings and don't cause startup failure.
func (a *App) preWarmSingleTenant(ctx context.Context, decls *messaging.Declarations) error {
	var errs []error

	errs = a.attemptDatabasePreWarm(ctx, errs)
	errs = a.attemptMessagingPreWarm(ctx, decls, errs)

	// Return combined errors but don't fail startup
	if len(errs) > 0 {
		return fmt.Errorf("pre-warming issues (non-fatal): %w", errors.Join(errs...))
	}

	return nil
}

// attemptDatabasePreWarm attempts to pre-warm the database connection.
func (a *App) attemptDatabasePreWarm(ctx context.Context, errs []error) []error {
	if a.dbManager == nil {
		a.logger.Debug().Msg("Skipping single-tenant database pre-warming: manager unavailable")
		return errs
	}

	if err := a.preWarmDatabase(ctx); err != nil {
		// Check if error is due to database not being configured
		if config.IsNotConfigured(err) {
			a.logger.Debug().Msg("Skipping single-tenant database pre-warming: not configured")
		} else {
			a.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant database connection")
			errs = append(errs, fmt.Errorf("database pre-warming failed: %w", err))
		}
	} else {
		a.logger.Info().Msg("Pre-warmed single-tenant database connection")
	}

	return errs
}

// attemptMessagingPreWarm attempts to pre-warm messaging components.
func (a *App) attemptMessagingPreWarm(ctx context.Context, decls *messaging.Declarations, errs []error) []error {
	if a.messagingManager == nil {
		a.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: manager unavailable")
		return errs
	}

	if err := a.preWarmMessaging(ctx, decls); err != nil {
		// Check if error is due to messaging not being configured
		if config.IsNotConfigured(err) {
			a.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: not configured")
		} else {
			a.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant messaging")
			errs = append(errs, fmt.Errorf("messaging pre-warming failed: %w", err))
		}
	} else {
		a.logger.Info().Msg("Pre-warmed single-tenant messaging")
	}

	return errs
}

// preWarmDatabase leases the fixed "" key to verify connectivity and releases it
// immediately. attemptDatabasePreWarm holds the manager nil check.
func (a *App) preWarmDatabase(ctx context.Context) error {
	_, release, err := a.dbManager.Get(ctx, "")
	if err != nil {
		return err
	}
	release() // pre-warm only verifies connectivity; release the lease immediately
	return nil
}

// preWarmMessaging ensures consumers for the fixed "" key and waits, bounded, for the
// publisher to report ready. attemptMessagingPreWarm holds the manager nil check.
func (a *App) preWarmMessaging(ctx context.Context, decls *messaging.Declarations) error {
	if decls != nil {
		if err := a.messagingManager.EnsureConsumers(ctx, "", decls); err != nil {
			return fmt.Errorf("failed to ensure consumers: %w", err)
		}
		a.logger.Info().Msg("Ensured messaging consumers")
	}

	client, release, err := a.messagingManager.Publisher(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to get publisher: %w", err)
	}
	defer release() // pre-warm only verifies connectivity; release the lease when done

	switch a.awaitPublisherReady(ctx, client) {
	case preWarmReady:
		a.logger.Info().Msg("Pre-warmed messaging publisher")
	case preWarmCanceled:
		// Startup abort / shutdown in flight — propagate the cancellation
		// instead of mislabeling it as a broker-readiness problem.
		return fmt.Errorf("publisher readiness wait canceled: %w", ctx.Err())
	default: // preWarmNotReadyInTime
		// Non-fatal: PublishToExchange's own readytimeout pre-flight (see
		// messaging/amqp_client.go) will still absorb a slow first real publish.
		a.logger.Warn().
			Dur("ready_timeout", a.publisherReadinessTimeout()).
			Msg("Messaging publisher not ready within pre-warm window; continuing startup")
	}

	return nil
}

// preWarmReadyOutcome reports why awaitPublisherReady's bounded wait ended.
// Mirrors messaging's unexported readyWaitOutcome (see messaging/amqp_client.go)
// minus the shutdown-channel case pre-warm has no equivalent for, so ctx
// cancellation is never conflated with a readiness timeout.
type preWarmReadyOutcome int

const (
	preWarmReady preWarmReadyOutcome = iota
	preWarmNotReadyInTime
	preWarmCanceled
)

// publisherReadinessTimeout resolves the readiness budget: the operator's
// messaging.reconnect.readytimeout when positive, the
// defaultPreWarmReadinessTimeout fallback otherwise. Nil-guarded because a
// directly-constructed App may carry no config.
func (a *App) publisherReadinessTimeout() time.Duration {
	if a.cfg != nil && a.cfg.Messaging.Reconnect.ReadyTimeout > 0 {
		return a.cfg.Messaging.Reconnect.ReadyTimeout
	}
	return defaultPreWarmReadinessTimeout
}

// awaitPublisherReady polls client.IsReady() until it reports ready, the
// bounded publisherReadinessTimeout elapses, or ctx is canceled — whichever
// comes first — and reports which of the three ended the wait. A readiness
// timeout never fails startup (preWarmMessaging logs a WARN and continues);
// cancellation propagates so shutdown isn't mislabeled as not-ready.
func (a *App) awaitPublisherReady(ctx context.Context, client messaging.AMQPClient) preWarmReadyOutcome {
	if client.IsReady() {
		return preWarmReady
	}

	timeout := time.NewTimer(a.publisherReadinessTimeout())
	defer timeout.Stop()
	ticker := time.NewTicker(preWarmReadinessPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return preWarmCanceled
		case <-timeout.C:
			return preWarmNotReadyInTime
		case <-ticker.C:
			if client.IsReady() {
				return preWarmReady
			}
		}
	}
}
```

- [ ] **Step 4: Rewire the call sites so the package compiles again**

**`app/lifecycle.go`** — in `prepareRuntime`, replace

```go
	if !a.cfg.Multitenant.Enabled && a.connectionPreWarmer != nil && a.connectionPreWarmer.IsAvailable() {
		a.connectionPreWarmer.LogAvailability()
		if err := a.connectionPreWarmer.PreWarmSingleTenant(ctx, decls); err != nil {
			a.logger.Warn().Err(err).Msg("Pre-warming completed with warnings")
		}
	}
```

with

```go
	// Single-tenant only, and only when there is something to warm — with neither
	// manager built the pass has nothing to do and stays silent.
	if !a.cfg.Multitenant.Enabled && (a.dbManager != nil || a.messagingManager != nil) {
		if err := a.preWarmSingleTenant(ctx, decls); err != nil {
			a.logger.Warn().Err(err).Msg("Pre-warming completed with warnings")
		}
	}
```

**`app/app.go`** — replace the field block Task 1 left behind

```go
	// Messaging declarations, plus the connection pre-warmer (database + messaging)
	messagingDeclarations *messaging.Declarations
	connectionPreWarmer   *ConnectionPreWarmer
```

with

```go
	// Messaging declarations, collected once at startup and replayed per tenant
	messagingDeclarations *messaging.Declarations
```

**`app/app_builder.go`** — in `ConfigureRuntimeHelpers`, delete these four lines (the ADR-050 untyped-connection-string guard above them and the `skipPreInit` block below them both stay):

```go
	b.app.connectionPreWarmer = NewConnectionPreWarmer(b.logger, b.app.dbManager, b.app.messagingManager)
	// Thread the operator's readiness budget (messaging.reconnect.readytimeout)
	// into the pre-warm wait. Set post-construction: NewConnectionPreWarmer is
	// shipped API and must keep its signature byte-identical (apidiff gate).
	b.app.connectionPreWarmer.readinessTimeout = b.cfg.Messaging.Reconnect.ReadyTimeout
```

**`app/app_test.go`** — in `newTestAppFixture`, delete the line

```go
		connectionPreWarmer: NewConnectionPreWarmer(log, dbManager, messagingManager),
```

**`app/lifecycle_test.go`** — delete both assignment lines, `a.connectionPreWarmer = NewConnectionPreWarmer(a.logger, dbManager, nil)` (in `TestPrepareRuntimePropagatesContextToPreWarm`) and `a.connectionPreWarmer = NewConnectionPreWarmer(rec, dbManager, nil)` (in `TestPrepareRuntimeWarnsOnlyWhenPreWarmFails`). The `a.dbManager = dbManager` line immediately above each is now the whole wiring.

**`app/app_builder_test.go`** — delete `TestAppBuilderConfigureRuntimeHelpersThreadsReadyTimeout` in full (there is no longer anything to thread; `TestAppPublisherReadinessTimeout` in `app/prewarm_test.go` is its replacement, pinning the same operator key at the resolver).

- [ ] **Step 5: Add the multi-tenant pre-warm skip guard**

The pre-warm gate has two halves and only one was pinned. Append to `app/lifecycle_test.go`, directly after `TestPrepareRuntimeWarnsOnlyWhenPreWarmFails`:

```go
// TestPrepareRuntimeSkipsPreWarmInMultiTenantMode pins the other half of the pre-warm
// gate. Multi-tenant resources resolve per tenant, so the fixed "" key must never be
// warmed at startup — a database whose config resolution always fails would emit the
// pre-warm WARN if it were.
func TestPrepareRuntimeSkipsPreWarmInMultiTenantMode(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: true},
	}
	rec := &recLogger{}
	a := newLifecycleCheckAppWithLogger(t, cfg, rec)

	connector := func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
		return dbtesting.NewTestDB(dbTypePostgres), nil
	}
	dbManager := database.NewDbManager(staticDBConfigProvider{err: errors.New(preWarmFailureMarker)}, rec,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute}, connector)
	t.Cleanup(func() { _ = dbManager.Close() })

	a.dbManager = dbManager

	require.NoError(t, a.prepareRuntime(context.Background()))

	_, emitted := loggedEvent(rec, preWarmWarnMsg)
	assert.False(t, emitted, "multi-tenant startup must not pre-warm the fixed \"\" key")
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && gofmt -w app/ && go test ./app/ -race`

Expected: `ok  	github.com/gaborage/go-bricks/app	<duration>`.

- [ ] **Step 7: Prove the old surface is gone**

Run:

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks && git grep -nE '(ConnectionPreWarmer|PreWarmSingleTenant|PreWarmDatabase|PreWarmMessaging|LogAvailability|IsAvailable|connectionPreWarmer|readinessTimeout)' -- '*.go'
```

Expected: no output (exit status 1). `client.IsReady()` does not match `IsAvailable`, and `publisherReadinessTimeout` does not match `readinessTimeout` because the grep is unanchored on a capital `R` — if it *does* match, the pattern found `readinessTimeout` inside it; re-read the hits before assuming a leftover.

- [ ] **Step 8: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
cat > /tmp/msg-task2.txt <<'MSG'
refactor(app)!: fold ConnectionPreWarmer into unexported App pre-warm methods

ConnectionPreWarmer held a logger and the two manager pointers App already holds,
was constructed by exactly one Builder step, and was driven from exactly one
prepareRuntime line. It becomes unexported methods on App.

The pass keeps its order (database, then messaging), its poll cadence, and its
tri-state publisher-readiness outcome: ready, not-ready-in-time (WARN, never fatal),
and caller-canceled (propagated, so a shutdown is not mislabeled as a broker
problem). Pre-warm remains advisory — its accumulated error only ever produces the
"Pre-warming completed with warnings" WARN.

The readiness budget is now read from cfg.Messaging.Reconnect.ReadyTimeout at use
time instead of being poked into a field after construction; that is the same value
ConfigureRuntimeHelpers threaded, and the comment justifying the poke (keeping a
shipped constructor signature byte-identical for apidiff) dies with the constructor.
IsAvailable and LogAvailability go: the gate is now the manager nil checks
prepareRuntime and the two attempt functions already perform, and their DEBUG lines
already report the same absence.

A new lifecycle test pins the previously unguarded half of the gate: multi-tenant
startup must not pre-warm the fixed "" key.

BREAKING CHANGE: app.ConnectionPreWarmer, app.NewConnectionPreWarmer and its
PreWarmSingleTenant, PreWarmDatabase, PreWarmMessaging, IsAvailable and
LogAvailability methods are removed. Nothing outside app/ referenced them.
MSG
git add app/prewarm.go app/prewarm_test.go app/lifecycle.go app/lifecycle_test.go app/app.go app/app_builder.go app/app_builder_test.go app/app_test.go
git commit -F /tmp/msg-task2.txt
git log -1 --format='%h %G? %s'
```

Expected: the commit lands with a good signature.

---

## Task 3: Delete the two dead `Options` fields and unexport the eight debug JSON types

**Files:**

- Modify: `app/options.go:13-14`
- Modify: `app/app_builder_test.go:212`
- Modify: `app/debug_handlers.go:18-59,319-320`, `app/debug_health.go:16-40,62`, `app/debug_goroutines.go:29,35,52-53,89-91,117,128,151,201,217-218,231,240,257,259,269,271,281,283,293,300,320,328,347,356`, `app/readiness_render.go:168-195`
- Modify: `app/debug_health_test.go:393,404,435-436,445`, `app/debug_goroutines_test.go` (all `GoroutineStack` literals), `app/readiness_render_test.go:134-364`
- Test: `app/debug_handlers_test.go` (new wire-shape pin)

**Interfaces:**

- Consumes: nothing from Tasks 1–2.
- Produces: the unexported type names later tasks' docs cite — `healthDebugInfo`, `componentHealth`, `healthSummary` (type), `debugResponse`, `gcInfo`, `goroutineInfo`, `goroutineStack`, `potentialLeak`. The existing **function** `healthSummary` in `app/readiness_render.go` is renamed `summarizeHealth` to free the name for the type.

- [ ] **Step 1: Write the JSON wire-shape pin**

The whole point of unexporting is that the wire contract does not move, so the pin must be keyed on JSON names only — naming `app`'s own types here would make the test track the rename instead of guarding it. Append to `app/debug_handlers_test.go`:

```go
// TestDebugJSONWireShapeIsStable pins the debug endpoints' wire contract by key name
// only. Decoding into anonymous maps is deliberate: referencing the response types
// would make this test follow a Go-side rename instead of catching one.
func TestDebugJSONWireShapeIsStable(t *testing.T) {
	app := &App{logger: logger.New("info", false)}
	handlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, app.logger)

	t.Run("gc_response", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/gc", http.NoBody)
		rec := httptest.NewRecorder()
		require.NoError(t, handlers.handleGC(server.NewHandlerContextForTest(rec, req, nil)))

		var envelope map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		assert.ElementsMatch(t, []string{"timestamp", "duration", "data"}, keysOf(envelope),
			"the debug envelope's key set is the wire contract (error is omitempty)")

		var data map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(envelope["data"], &data))
		assert.ElementsMatch(t,
			[]string{"stats", "mem_before", "mem_after", "forced", "heap_objects", "heap_size"},
			keysOf(data))
	})

	t.Run("goroutines_response", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/goroutines", http.NoBody)
		rec := httptest.NewRecorder()
		require.NoError(t, handlers.handleGoroutines(server.NewHandlerContextForTest(rec, req, nil)))

		var envelope struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		assert.ElementsMatch(t, []string{"count", "by_state", "by_function"}, keysOf(envelope.Data),
			"stacks and potential_leaks are omitempty and absent without ?stacks / ?leaks")
	})
}
```

`keysOf` is the generic helper already defined at the bottom of `app/debug_health_test.go`. Add whatever of `encoding/json`, `net/http`, `net/http/httptest`, `context` and `github.com/gaborage/go-bricks/server` the import block is missing.

- [ ] **Step 2: Run it, then prove it can fail**

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && go test ./app/ -run TestDebugJSONWireShapeIsStable -race -v`

Expected: PASS — it characterizes today's JSON.

Now watch it fail, because a characterization test you never saw fail proves nothing. In `app/debug_handlers.go`, temporarily change `MemBefore uint64 \`json:"mem_before"\`` to `json:"membefore"` and re-run:

Expected: FAIL with

```text
    debug_handlers_test.go:NNN:
        	Error:      elements differ
        	            extra elements in list A:
        	            ([]string) (len=1) { (string) (len=10) "mem_before" }
        	            extra elements in list B:
        	            ([]string) (len=1) { (string) (len=9) "membefore" }
```

Revert the tag and re-run — expected PASS.

- [ ] **Step 3: Delete the two dead `Options` fields**

In `app/options.go`, delete the first two lines of the struct:

```go
	Database               database.Interface
	MessagingClient        messaging.Client
```

`database` is still imported for `DatabaseConnector`; `messaging` is still imported for `MessagingClientFactory` — leave both imports alone.

In `app/app_builder_test.go:207-217`, the `options_present_but_filter_nil_uses_config` sub-test names the removed field to build a populated `Options`. Replace

```go
		// A populated Options struct that doesn't set LoggerFilterConfig must not
		// short-circuit the config path — typical for apps that configure
		// Database/Server via Options and masking via YAML.
		got := resolveLoggerFilterConfig(
			&Options{Database: nil}, // LoggerFilterConfig left zero
			&config.LogConfig{SensitiveFields: []string{"pan"}},
		)
```

with

```go
		// A populated Options struct that doesn't set LoggerFilterConfig must not
		// short-circuit the config path — typical for apps that configure a
		// messaging factory via Options and masking via YAML.
		got := resolveLoggerFilterConfig(
			&Options{ // LoggerFilterConfig left zero
				MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient { return nil },
			},
			&config.LogConfig{SensitiveFields: []string{"pan"}},
		)
```

- [ ] **Step 4: Unexport the eight types**

Apply this rename map across `app/`. The `json:` tags stay byte-identical — only the Go identifiers change.

| Exported | Unexported |
| --- | --- |
| `HealthDebugInfo` | `healthDebugInfo` |
| `ComponentHealth` | `componentHealth` |
| `HealthSummary` | `healthSummary` |
| `DebugResponse` | `debugResponse` |
| `GCInfo` | `gcInfo` |
| `GoroutineInfo` | `goroutineInfo` |
| `GoroutineStack` | `goroutineStack` |
| `PotentialLeak` | `potentialLeak` |

Three name collisions must be resolved in the same edit — a mechanical `sed` alone will not compile:

1. **`healthSummary` is already a function** at `app/readiness_render.go:194`. Rename that function to `summarizeHealth` (it aggregates a components map into the summary, so the verb reads better anyway). Its two call sites are `app/debug_health.go:64` (`Summary: healthSummary(components)` → `Summary: summarizeHealth(components)`) and `app/readiness_render_test.go:364` (`assert.Equal(t, tt.wantSummary, healthSummary(components))` → `summarizeHealth(components)`). Its doc comment's first word must change with it.
2. **`gcInfo` collides with local variables** at `app/debug_goroutines.go:320` and `:347` (`gcInfo := &GCInfo{…}`, then `d.newDebugResponse(start, gcInfo, nil)`). Rename both locals to `info`.
3. **`goroutineInfo` collides with a local variable** at `app/debug_goroutines.go:29` (`goroutineInfo, err := d.analyzeGoroutines(...)`, used at `:35`). Rename that local to `info`.

Also update each type's doc comment to start with the new lowercase identifier (`// healthDebugInfo contains enhanced health information for debugging`, etc.) — a comment that still opens with `HealthDebugInfo` names a symbol that no longer exists, and every other unexported type in `app/` follows the convention.

`Info` (`app/debug_health.go:43`) stays exported: it is not on the kill list.

A safe order: rename the function first (step 1 above), then run one `sed` per type over `app/*.go`, then fix the three local variables.

- [ ] **Step 5: Run the full package to verify it passes**

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && gofmt -w app/ && go vet ./app/ && go test ./app/ -race`

Expected: `ok  	github.com/gaborage/go-bricks/app	<duration>` with no vet output. `go vet` matters here specifically: it compiles the test files that `go build` would skip, and `app/debug_goroutines_test.go` carries dozens of `GoroutineStack` literals.

- [ ] **Step 6: Prove the exported names are gone**

Run:

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks && git grep -nE '(HealthDebugInfo|ComponentHealth|HealthSummary|DebugResponse|GCInfo|GoroutineInfo|GoroutineStack|PotentialLeak)' -- '*.go' && echo 'LEFTOVERS ABOVE'
```

Expected: no output and no `LEFTOVERS ABOVE` line (the `git grep` exits 1, short-circuiting the `&&`).

- [ ] **Step 7: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
cat > /tmp/msg-task3.txt <<'MSG'
refactor(app)!: drop two unread Options fields and unexport the debug JSON types

Options.Database and Options.MessagingClient were read by nothing — not by the
bootstrap, not by the factory resolver, not by any consumer. Every dependency they
were meant to inject arrives through DatabaseConnector, MessagingClientFactory or
ResourceSource instead.

The eight debug response types (HealthDebugInfo, ComponentHealth, HealthSummary,
DebugResponse, GCInfo, GoroutineInfo, GoroutineStack, PotentialLeak) describe the
shape of two access-controlled endpoints' JSON; nothing outside app/ names them.
They become unexported with their json tags byte-identical, so the wire contract
does not move — a new test pins that contract by key name only, decoding into
anonymous maps so it cannot follow a Go-side rename.

readiness_render.go's healthSummary FUNCTION is renamed summarizeHealth to free the
name for the type; two GC locals and one goroutine local are renamed to info for
the same reason.

BREAKING CHANGE: app.Options loses its Database and MessagingClient fields, and the
eight debug response types are no longer exported. The JSON they emit is unchanged.
MSG
git add app/options.go app/debug_handlers.go app/debug_health.go app/debug_goroutines.go app/readiness_render.go app/debug_handlers_test.go app/debug_health_test.go app/debug_goroutines_test.go app/readiness_render_test.go app/app_builder_test.go
git commit -F /tmp/msg-task3.txt
git log -1 --format='%h %G? %s'
```

Expected: the commit lands with a good signature.

---

## Task 4: Documentation — ADR-067, index + counter, atom `[C60.4]`, CLAUDE.md, two wiki wording fixes

**Files:**

- Create: `wiki/adr_067_lifecycle_slots.md`
- Modify: `wiki/architecture_decisions.md` (index entry after ADR-066's block; the `ADR-001 through ADR-066` counter in "Numbering Policy")
- Modify: `wiki/migrations.md` (the `E60` hop-table row; a new `[C60.4]` atom under `## E60`; the `[C57.8]` scope and ref lines)
- Modify: `wiki/startup_defaults.md:82`
- Modify: `CLAUDE.md` (one `## Breaking Changes` line)

**Interfaces:**

- Consumes: the unexported names Tasks 1–3 produced (`App.prepareRuntimeConsumers`, `App.awaitPublisherReady`) — the wiki lines below cite them by those exact spellings.

> **Rebase note for the controller.** PR #1041 also opens hop `E60` with atoms `C60.1` and `C60.2`, and PR1b's `[C60.3]` is already there. This PR's atom is deliberately numbered **`[C60.4]`** so the four do not collide. If #1041 and this PR land in a different order than assumed, the atom bodies still merge cleanly (they are separate `###` blocks) but the **E60 hop-table row must be merged by hand**: it is a single table line, so git will conflict on it. Reconcile by (a) adding `compile-break (C60.4)` to its "worst risk" cell, (b) setting the "atoms" count to the number of `### [C60.*]` blocks actually present, (c) adding `C60.4` to the "compiler-caught" cell, and (d) appending this atom's preflight sentence to the preflight cell.

- [ ] **Step 1: Write ADR-067**

Create `wiki/adr_067_lifecycle_slots.md`:

```markdown
# ADR-067: Slots Own the Per-Kind Lifecycle

**Status:** Accepted
**Date:** 2026-08-17
**Builds on:** [ADR-066](adr_066_readiness_one_module.md) (readiness is one module — a
*probe description* is what a slot hands readiness), [ADR-045](adr_045_no_producer_side_manager_interfaces.md)
(no producer-side manager interface), [ADR-029](adr_029_graceful_shutdown_order.md)
(shutdown order) — all preserved.

## Context

Every resource kind's application lifecycle facts — construct, expose, pre-init, probe,
maintenance, close, `/ready` render, debug render — live in roughly ten `app/` files that
each hand-enumerate the kind set. ADR-066 collapsed the readiness half of that: one module
judges every kind from a probe description. The other phases did not move, so adding a
kind still means editing every place that enumerates kinds. Adding the streams kind (#973)
touched six files and still needed a runtime-registration exception, because the streams
manager does not exist while the Builder runs. Seven of the last twenty `app/` commits
landed in this cluster.

The same shape also grew two pass-through helpers. `MessagingInitializer` and
`ConnectionPreWarmer` each held a logger plus manager pointers `App` already holds, each
was constructed by one Builder step, and each was driven from one `prepareRuntime` line.
Neither was referenced anywhere outside `app/`. Alongside them sat two `Options` fields no
code read and eight exported response types describing the JSON of two access-controlled
debug endpoints.

## Decision

A **slot** owns one resource kind's whole application lifecycle.

1. **Slot shape.** An unexported `resourceSlot` interface implemented by four unexported
   per-kind structs in `app/` — database, messaging, cache, streams — so the compiler
   checks completeness. `App` keeps its typed manager fields (`ResourceProvider` needs the
   concrete types), and every lifecycle walk iterates the slot list rather than
   re-enumerating kinds.
2. **Phases.** `probe()` returns the kind's probe description · `preInit(ctx)` carries a
   `fatal` flag (database and messaging fatal, cache best-effort, streams none) ·
   `start(ctx)` runs in `prepareRuntime` (messaging: ensure consumers, the #907
   fail-vs-warn grading, and the bounded await-publisher-ready; streams: construct the
   manager and `Manager.Start`; database: the single-tenant pre-warm, with the mode check
   inside the slot; cache: nothing) · `stop(ctx)` runs before module `Shutdown` (messaging
   and streams stop consumers) · `close()` runs after (all four). There is no maintenance
   phase.
3. **One registration order:** `database → messaging → cache → streams`, used by every
   phase and by probe order. Close stays FIFO, so ADR-029's shutdown ordering is preserved
   by the stop/close split rather than by a second list.
4. **Maintenance is manager-side.** `database.DbManager` and `messaging.Manager` self-start
   their idle-cleanup loop at construction when `IdleTTL > 0`, exactly as
   `cache.NewCacheManager` already does, and stop it in `Close()`. `StartCleanup` stays
   exported and becomes idempotent, `StopCleanup` stays, and the
   cleanup-interval-vs-idle-TTL WARN moves beside the pool that owns both values.
   `App.startMaintenanceLoops`, `App.warnIfCleanupIntervalTooLate` and
   `App.shutdownManagers` delete.
5. **Builder steps keep their names.** `CreateHealthProbes` and `RegisterClosers` iterate
   slots instead of hand-listing managers; renaming them belongs to the separate "Builder
   collapse" candidate, not here.
6. **The streams slot exists at build time with a nil manager** (its probe reads
   `disabled`) and constructs plus starts its manager in `start`. That removes the
   runtime-registration exception without moving stream construction earlier than the
   declarations that size it.

Slots name only what `app` calls. Consistent with ADR-045, no producer package grows a
manager interface for this — the interface lives in `app/`, where the caller is.

## Delivery

Four stacked PRs, each under ~400 changed LoC and each self-contained:

- **PR2 (this ADR's own PR) deletes the pass-through helpers** so the slot work starts from
  a clean surface: `MessagingInitializer` and `ConnectionPreWarmer` fold into unexported
  `App` methods, two unread `Options` fields go, and eight debug response types are
  unexported. No behavior changes.
- **PR3** introduces `resourceSlot` and the four structs, and converts pre-init, probe and
  close to slot iteration.
- **PR4** moves maintenance manager-side (decision 4).
- **PR5** gives streams its `start` phase, folding `app/streams_setup.go` into the streams
  slot.

## Consequences

- **Removed in PR2** (nothing outside `app/` referenced any of them):
  `MessagingInitializer`, `NewMessagingInitializer`, `CollectDeclarations`,
  `SetupLazyConsumerInit`, `PrepareRuntimeConsumers`, `LogDeploymentMode`,
  `MessagingInitializer.IsAvailable`, `ConnectionPreWarmer`, `NewConnectionPreWarmer`,
  `PreWarmSingleTenant`, `PreWarmDatabase`, `PreWarmMessaging`, `LogAvailability`,
  `ConnectionPreWarmer.IsAvailable`, `Options.Database`, `Options.MessagingClient`.
  Unexported with their JSON unchanged: `HealthDebugInfo`, `ComponentHealth`,
  `HealthSummary`, `DebugResponse`, `GCInfo`, `GoroutineInfo`, `GoroutineStack`,
  `PotentialLeak`.
- **What consumers do: nothing**, unless a service named one of those types in its own Go
  code — building an `app.ConnectionPreWarmer` by hand, embedding `app.GCInfo`, or setting
  `Options.Database`. Those break at compile time and have no replacement, because the
  framework drives every one of these paths itself. `/ready`, `/_sys/health-debug`, the
  goroutine and GC endpoints all emit byte-identical JSON.
- **`SetupLazyConsumerInit` was already redundant.** `App.buildMessagingDeclarations` pushes
  the declaration set into the resource provider through the `declarationSetter` seam,
  which is a superset: it covers both concrete providers and any third implementation,
  where `SetupLazyConsumerInit` type-switched on two and merely warned about the rest.
- **Locality.** After PR5, adding a resource kind is one slot file, not an edit in ten
  places, and the compiler — not review — enforces that every phase was considered.
- **Cost.** The slot interface is indirection that a two-kind framework would not earn.
  Four kinds, five phases and ten enumeration sites is what earns it; the alternative
  measured against it is "keep hand-enumerating", which is what produced the drift ADR-066
  documents.
- **Watch:** `StartCleanup` becoming idempotent (PR4) means a caller that starts it twice
  no longer leaks a goroutine, but a caller that relied on a *second* call changing the
  interval must call `StopCleanup` first. That lands with PR4's own atom, not this one.
```

- [ ] **Step 2: Add the index entry and bump the counter**

In `wiki/architecture_decisions.md`, insert this block immediately after the ADR-066 entry's closing `---` and before `## ADR Lifecycle`:

```markdown
### [ADR-067: Slots Own the Per-Kind Lifecycle](adr_067_lifecycle_slots.md)

**Date:** 2026-08-17 | **Status:** Accepted

Every resource kind's lifecycle facts — construct, expose, pre-init, probe, maintenance,
close, render — live in about ten `app/` files that each hand-enumerate the kind set, which
is why adding the streams kind touched six files and still needed a runtime-registration
exception. A **slot** owns one kind's whole lifecycle: an unexported `resourceSlot`
interface with four per-kind structs (compiler-checked completeness), phases
`probe · preInit(fatal) · start · stop · close`, one registration order
`database → messaging → cache → streams` with FIFO close, and maintenance moved
manager-side so `DbManager`/`messaging.Manager` self-start idle cleanup at construction.
`App` keeps its typed manager fields; the Builder steps keep their names and iterate slots;
the streams slot exists at build time with a nil manager and constructs in `start`. Ships
as four stacked PRs — the first deletes the pass-through helpers the slots replace.

**Key Benefits:** Adding a resource kind becomes one slot file instead of ten edits, and
the compiler enforces that every phase was considered. **Watch:** the first PR removes
sixteen `app` symbols nothing outside `app/` referenced (`MessagingInitializer` and
`ConnectionPreWarmer` and their methods, `Options.Database`, `Options.MessagingClient`) and
unexports eight debug response types with their JSON unchanged. See
[migrations.md](migrations.md) `[C60.4]`.

---
```

Then, in the "Numbering Policy" paragraph, change `ADR numbers (ADR-001 through ADR-066)` to `ADR numbers (ADR-001 through ADR-067)`.

- [ ] **Step 3: Add atom `[C60.4]` and update the E60 hop row**

In `wiki/migrations.md`, append this atom **after** the `[C60.3]` block and **before** the `*The sections below are reference material…*` line:

```markdown
---

### [C60.4] sixteen unused `app` symbols removed; eight debug response types unexported · compile-break · when: match

- detect: three greps, all against your own Go code —
  `git grep -nE 'app\.(MessagingInitializer|NewMessagingInitializer|ConnectionPreWarmer|NewConnectionPreWarmer|PreWarmSingleTenant|PreWarmDatabase|PreWarmMessaging|LogAvailability|LogDeploymentMode|CollectDeclarations|SetupLazyConsumerInit|PrepareRuntimeConsumers)([^A-Za-z0-9_]|$)' -- '*.go'`,
  `git grep -nE 'app\.(HealthDebugInfo|ComponentHealth|HealthSummary|DebugResponse|GCInfo|GoroutineInfo|GoroutineStack|PotentialLeak)([^A-Za-z0-9_]|$)' -- '*.go'`, and
  `git grep -nE 'app\.Options\{' -- '*.go'` — then read each `Options` literal for a
  `Database:` or `MessagingClient:` field. Include test files: `go build ./...` does not
  compile them, so a hit there surfaces only under `go vet ./...` or `go test`.
- scope: none of these were ever called by the framework's own consumers — they were
  internal helpers that happened to be exported. `MessagingInitializer` and
  `ConnectionPreWarmer` held a logger plus manager pointers `app.App` already holds and
  were each driven from a single startup line; they are now unexported `App` methods
  (`prepareRuntimeConsumers`, `preWarmSingleTenant` and friends). `Options.Database` and
  `Options.MessagingClient` were read by no code path at all. The eight debug response
  types describe the JSON of `/_sys/health-debug`, `/_sys/goroutines` and `/_sys/gc`; they
  are unexported with their `json:` tags byte-identical. **No emitted JSON, log line,
  status code or startup behavior changes** — including the #907 fail-vs-warn consumer
  grading ([C57.8]) and the pre-warm publisher-readiness wait, both of which keep their
  exact semantics at their new unexported home.
- gate: match = at least one grep names one of these symbols on the `app` package, or an
  `app.Options` literal sets `Database:` or `MessagingClient:`. no-match = the common case;
  a service that only registers modules and calls `app.New*` never touched any of them.
- apply: there is no replacement for the removed helpers — the framework drives every one
  of those paths itself, so delete the call. Concretely: an `app.NewConnectionPreWarmer`
  built to warm a connection yourself is redundant with `app.App`'s own single-tenant
  pre-warm; an `app.NewMessagingInitializer` is redundant with `prepareRuntime`; a
  `Database:` or `MessagingClient:` field in an `app.Options` literal was inert and should
  be dropped (inject through `DatabaseConnector`, `MessagingClientFactory` or
  `ResourceSource` instead); a variable typed `app.GCInfo`/`app.ComponentHealth`/… to
  decode a debug endpoint should decode into your own struct with the same `json` tags —
  the wire shape is unchanged, so copying the tags is a mechanical move.
- verify: `go build ./... && go vet ./...` — vet is the load-bearing half, since a
  reference in a `_test.go` file is invisible to `go build`.
- ref: [ADR-067](adr_067_lifecycle_slots.md)
```

Then update the `E60` row of the hop table at the top of the file. Its cells become:

- **worst risk:** `compile-break (C60.4 — internal helpers nothing outside app/ used) + silent-behavior (C60.3 changes strings on /ready's 200 body and on the debug health view; no status code moves)`
- **atoms:** the current number **+1**
- **compiler-caught:** `C60.4`
- **preflight:** append to the existing sentence: ` Also grep your Go code — test files included — for the sixteen removed app helpers and the eight unexported debug response types (C60.4).`

And extend the hop's `- gist:` bullet with a final sentence: `Sixteen unused exported app symbols are also removed and eight debug response types unexported, with no change to any emitted JSON (C60.4).`

- [ ] **Step 4: Fix the two stale wiki references**

**`wiki/startup_defaults.md:82`** — replace the fragment

```text
The wait (`ConnectionPreWarmer.awaitPublisherReady`) is context-aware
```

with

```text
The wait (`App.awaitPublisherReady`, `app/prewarm.go`) is context-aware
```

**`wiki/migrations.md`, atom `[C57.8]`** — replace the scope line's opening

```text
- scope: `MessagingInitializer.PrepareRuntimeConsumers` previously logged one WARN
```

with

```text
- scope: single-tenant consumer bootstrap (`MessagingInitializer.PrepareRuntimeConsumers`
  at v0.57.0; the unexported `App.prepareRuntimeConsumers` since v0.60.0, [C60.4])
  previously logged one WARN
```

and replace that atom's `ref:` line

```text
- ref: `app/messaging_setup.go` (`PrepareRuntimeConsumers`) · `app/lifecycle.go`
  (`prepareMessagingConsumers`, `assertMessagingConfiguredIfDeclared`)
```

with

```text
- ref: `app/messaging_setup.go` (`prepareRuntimeConsumers`) · `app/lifecycle.go`
  (`prepareRuntime`, `assertMessagingConfiguredIfDeclared`)
```

- [ ] **Step 5: Add the CLAUDE.md Breaking Changes line**

CLAUDE.md is already over its 40,960-byte ceiling, so this must be one terse line. Append it directly after the `keystore.secretminlength tri-state (ADR-065)` bullet, as the last entry of the `## Breaking Changes` list:

```markdown
- **Dead app lifecycle surface removed (ADR-067):** `MessagingInitializer` and `ConnectionPreWarmer` (constructors and methods included), `Options.Database` and `Options.MessagingClient` are gone; the eight debug response types are unexported with their JSON unchanged.
```

- [ ] **Step 6: Verify the docs are internally consistent**

Run:

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
git grep -n 'ADR-001 through ADR-06' wiki/architecture_decisions.md
git grep -c 'adr_067_lifecycle_slots.md' wiki/architecture_decisions.md wiki/migrations.md
git grep -nE '(ConnectionPreWarmer\.awaitPublisherReady|prepareMessagingConsumers)' -- wiki/ CLAUDE.md llms.txt
make lint-md
```

Expected: the first prints `ADR-001 through ADR-067` (one line); the second prints `1` for each of the two files; the third prints nothing (exit 1) — the stale spellings are gone; `make lint-md` reports `Summary: 0 error(s)`. Do **not** pass file globs to `markdownlint-cli2` by hand — `.markdownlint-cli2.jsonc` owns both the globs and the ignores (see the Makefile note).

- [ ] **Step 7: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
cat > /tmp/msg-task4.txt <<'MSG'
docs(app): record ADR-067 and migration atom C60.4 for the removed surface

ADR-067 states the target the removals clear the way for: a slot owns one resource
kind's whole application lifecycle — an unexported resourceSlot interface with four
per-kind structs, phases probe/preInit/start/stop/close, one registration order
(database, messaging, cache, streams) with FIFO close, and maintenance moved
manager-side. It records the four-PR delivery, of which this PR is the first, and the
ADR-045 alignment: the interface lives in app/, where the caller is, so no producer
package grows a manager interface for it.

Atom C60.4 covers the compile break for the population that could hit one: three
greps, an explanation that none of these had a framework-side caller, and the note
that go vet — not go build — is what surfaces a reference from a test file. The E60
hop row gains the compile-break class and the extra atom.

Two stale wiki references are repointed at the unexported homes, and C57.8's scope
line now names both the v0.57.0 symbol and today's, so the historical atom stays
accurate without citing a symbol that no longer exists.
MSG
git add wiki/adr_067_lifecycle_slots.md wiki/architecture_decisions.md wiki/migrations.md wiki/startup_defaults.md CLAUDE.md
git commit -F /tmp/msg-task4.txt
git log -1 --format='%h %G? %s'
```

Expected: the commit lands with a good signature.

---

## Task 5: Controller gates

**This task is the controller's, not an implementer's.** Run in order, per CLAUDE.md.

- [ ] **Step 1: Full build and test**

Run (background): `cd /Users/gaborage/Projects/gaborage/code/go-bricks && pwd && make check`

Expected: `make check` passes — fmt, lint (golangci-lint v2), markdownlint, `go test ./... -race`, alloc guards, govulncheck, gosec. Watch specifically for `unused` on anything the deletions orphaned, and for `staticcheck ST1021` if a doc comment still starts with an old exported name.

- [ ] **Step 2: Confirm the whole kill list actually landed**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
git grep -nE '(MessagingInitializer|NewMessagingInitializer|CollectDeclarations|SetupLazyConsumerInit|LogDeploymentMode|PrepareRuntimeConsumers|ConnectionPreWarmer|NewConnectionPreWarmer|PreWarmSingleTenant|PreWarmDatabase|PreWarmMessaging|LogAvailability|HealthDebugInfo|ComponentHealth|HealthSummary|DebugResponse|GCInfo|GoroutineInfo|GoroutineStack|PotentialLeak)' -- '*.go'
```

Expected: no output at all.

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
git grep -nE '(ManagerConfigBuilder|ResourceManagerFactory|FactoryResolver|LogFactoryInfo|SetDeclarations|SignalHandler|TimeoutProvider|IPWhitelist)' -- 'app/*.go' | wc -l
```

Expected: a non-zero count — the LEAVE list is intact.

- [ ] **Step 3: Pre-push gates, in order**

`/simplify` → `make check` if it changed code → `/security-audit` → `make check` if it changed code → `/code-review` (CodeRabbit). Point the security audit at the one thing this diff genuinely moves: the pre-warm readiness budget now resolves from `a.cfg` at use time instead of from a field poked at build time, and the consumer-bootstrap guard collapsed from two layers to one.

- [ ] **Step 4: Mutation gate**

Commit first (the gate scopes to `merge-base..HEAD`), then run in the background:

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && pwd && make mutate`

Expected: `(N mutants on changed lines)` with N > 0 and no survivors. An empty or `no mutatable changes` result is **not** a pass. The likely survivors to check for: the `a.cfg != nil` guard in `publisherReadinessTimeout` (covered by the `nil_config_falls_back_to_default` case), the `len(decls.Consumers()) > 0` grading boundary (covered by the fails-vs-warns pair), and the two nil checks in the pre-warm gate.

- [ ] **Step 5: Push and open the PR**

This is PR2 of Stack A; its base is PR1b's branch (`feature/app-readiness-one-body-one-gate`). Use `/gh-stack`. **The PR title must carry the `!` marker** — e.g. `refactor(app)!: kill the dead app lifecycle surface` — or the `apidiff` job fails on the new INCOMPATIBLE changes and release-please derives the wrong bump. Remember CodeRabbit skips stacked PRs whose base is not `main`: post `@coderabbitai review` on this PR explicitly after opening it.

---

## Self-Review

**1. Spec coverage.** Walking spec decision 6 (the only decision PR2 executes) item by item:

| Spec item | Task |
| --- | --- |
| delete `MessagingInitializer`, `NewMessagingInitializer`, `CollectDeclarations`, `SetupLazyConsumerInit`, `LogDeploymentMode`, `PrepareRuntimeConsumers`, `MessagingInitializer.IsAvailable` | Task 1 |
| grading moves (spec says "into the messaging slot's `start`" — PR3; PR2 parks it at `App.prepareRuntimeConsumers`) | Task 1, Steps 1/3 |
| delete `ConnectionPreWarmer`, `NewConnectionPreWarmer`, `PreWarmSingleTenant`, `PreWarmDatabase`, `PreWarmMessaging`, `LogAvailability`, `ConnectionPreWarmer.IsAvailable` | Task 2 |
| delete `Options.Database`, `Options.MessagingClient` | Task 3, Step 3 |
| unexport the eight debug JSON types, JSON unchanged | Task 3, Steps 1/4 |
| update `wiki/startup_defaults.md:82`, `wiki/migrations.md` | Task 4, Step 4 |
| LEAVE list untouched | Global Constraints + Task 5, Step 2 |
| "ADR-067 + one atom listing every removed symbol land with PR2" | Task 4, Steps 1/3 |
| ADR-067 records decisions 1–5 and 7 (slot shape, phases, order, maintenance, Builder names, streams slot, delivery slices) | Task 4, Step 1 |

Spec decisions 1–5 and 7 are *recorded*, not implemented, in this PR — that is exactly what the brief asks. No gap.

**2. Placeholder scan.** No "TBD", no "similar to Task N", no "add error handling". Every code step carries the complete text to write; the two full-file rewrites are given in full rather than as diffs, because a reader picking up Task 2 alone cannot reconstruct `app/prewarm.go` from a diff. The one deliberately non-literal instruction is Task 3 Step 4's rename table — it is a mechanical eight-way rename over ~60 sites, so the table plus the three named collisions (with exact file:line) is more usable than sixty inline snippets, and Step 6's grep proves completeness.

**3. Type consistency.** `prepareRuntimeConsumers(ctx, decls)` is spelled identically in Task 1's test file, its implementation, `prepareRuntime`'s new call, the C57.8 ref line and the ADR. `preWarmSingleTenant(ctx, decls)`, `attemptDatabasePreWarm(ctx, errs)`, `attemptMessagingPreWarm(ctx, decls, errs)`, `preWarmDatabase(ctx)`, `preWarmMessaging(ctx, decls)`, `publisherReadinessTimeout()` and `awaitPublisherReady(ctx, client)` match between Task 2's Interfaces block, its test file and its implementation — note that `preWarmDatabase` and `preWarmMessaging` **drop the `key string` parameter** their exported ancestors had, since `""` was the only value either ever received, and both test and implementation reflect that. `summarizeHealth` replaces the `healthSummary` **function** at all three sites (`app/readiness_render.go`, `app/debug_health.go:64`, `app/readiness_render_test.go:364`) while `healthSummary` becomes the **type** — the one place a careless rename would produce a redeclaration. `newConsumerBootstrapApp` (Task 1) and `newPreWarmApp` (Task 2) are distinct helpers in distinct files with no name clash; `fakeBrokerURLProvider` is declared once, in `app/prewarm_test.go`, and used from three files.

**4. Ordering.** Each task leaves `app/` compiling and green on its own, so a reviewer can reject Task 3 while approving Tasks 1–2. Task 1 and Task 2 both edit the same three-line field block in `app/app.go` and the same `ConfigureRuntimeHelpers` region in `app/app_builder.go`; the intermediate states are spelled out verbatim in both tasks so the second does not have to guess what the first left.
