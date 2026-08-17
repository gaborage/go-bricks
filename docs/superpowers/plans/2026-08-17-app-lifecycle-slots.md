# Slots own the per-kind lifecycle — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce the unexported `resourceSlot` interface and four per-kind slots in `app/`, and make every lifecycle walk — probe, pre-init, start, stop, close — iterate one slot list in one registration order (`database → messaging → cache → streams`) instead of hand-enumerating the kinds at eight call sites.

**Architecture:** Four unexported structs (`databaseSlot`, `messagingSlot`, `cacheSlot`, `streamsSlot`) each hold a `*App` pointer plus the one verdict App cannot reach (`cacheAbsent`, which needs `Options`). They are built once by `Builder.CreateApp` into `App.slots []resourceSlot` and read live App state thereafter, so a test that swaps a manager in or out needs no rebuild of the slots themselves. `Builder.CreateHealthProbes`, `Builder.RegisterClosers`, `Builder.performPreInitialization`, `App.prepareRuntime` and `App.Shutdown` each collapse into a loop over that list. `App` keeps its typed manager fields — `ResourceProvider` needs the concrete types — and the exported `Prober`/`HealthStatus` seam is untouched.

**Tech Stack:** Go 1.26, testify (`assert`/`require`), zerolog, `golangci-lint` v2 via `make check`. No new dependencies. No new exported symbols.

**Spec:** `docs/superpowers/specs/2026-08-16-app-readiness-and-lifecycle-slots-design.md` — "Lifecycle slots (card 3)", decisions 1–5 and 7. Vocabulary: `CONTEXT.md` ("Slot", "Probe description", "Readiness"). Preceding ADRs: `wiki/adr_066_readiness_one_module.md` (PR1a/PR1b — **do not touch**), `wiki/adr_029_graceful_shutdown_order.md`, `wiki/adr_045_no_producer_side_manager_interfaces.md`. Preceding plan (already merged/in flight, read for the shapes it produced): `docs/superpowers/plans/2026-08-17-app-kill-dead-lifecycle-surface.md`.

**Stack position:** Stack A PR3, stacked on `feature/app-kill-dead-lifecycle-surface` (PR2), shipped as two PRs at the size note's split point: **PR3a** (`feature/app-slots-probe-preinit-close`) carries Tasks 1–2 — the slot interface, the probe walk, the close walk and the pre-init walk — and **PR3b** (`feature/app-slots-start-stop`, stacked on PR3a) carries Tasks 3–4 — the start and stop phases plus the verification sweep. Each PR passes `make check` and the mutation gate on its own. PR4 moves maintenance manager-side; PR5 folds `app/streams_setup.go`'s body into `streamsSlot.start`.

## Global Constraints

Copied verbatim from the controller's brief and CLAUDE.md. Every task's requirements implicitly include this section.

- **Behavior is identical except where this plan explicitly lists a change** (each listed change carries its own justification below, under "Decisions taken up front"). No change in this PR reaches an atom line — see Task 4.
- **Shutdown order per ADR-029 is preserved:** stop inbound work (HTTP server, then per-kind `stop`) → module `Shutdown` → observability → manager cleanup loops → closers.
- **ADR-045 is respected:** no producer-side manager interface. `resourceSlot` lives in `app/` and names only what `app` calls; `database`, `messaging`, `cache` and `messaging/streams` are not touched by this PR.
- **`Builder.CreateHealthProbes` and `Builder.RegisterClosers` keep their names.** Renaming Builder steps belongs to the "Builder collapse" candidate, which is out of scope.
- **No exported API additions.** Everything this PR introduces is unexported. `Prober`, `HealthStatus`, `Options`, `SignalHandler`, `TimeoutProvider` and every Builder step signature stay byte-identical.
- **No exported symbol is deleted.** If an executor finds one that must go, they stop and report it rather than deleting it — a deletion would turn this PR breaking and require an ADR plus a migration atom.
- **Do not touch `startMaintenanceLoops`, `warnIfCleanupIntervalTooLate`, `cleanupIntervalTooLate` or `shutdownManagers`.** PR4 owns them. There is no maintenance phase on the slot interface.
- **Do not touch `wiki/adr_066_readiness_one_module.md`, `app/readiness.go`'s probe constructors, or `app/readiness_render.go`.** The readiness module already judges and renders descriptions; this PR only changes who hands it one.
- **Test names:** camelCase for test function names (`TestCollectProbesRegistersTheThreeClassicKinds`, never `Test_Collect_Probes`). Table-driven case names use snake_case (`{name: "fatal_kind_stops_the_walk"}`).
- **Tests: replace, don't layer.** `rebuildClosersAndHealth` (which re-implements two Builder steps) is deleted; `TestPreInitCacheSkipsAbsentCache`, `TestPreWarmSingleTenantSkipsAbsentManagers` and the streams probe/closer assertions in `app/streams_setup_test.go` move to their new homes rather than being duplicated.
- **Commits:** `git commit -F <file>` with a message file — the commit hook blocks heredoc `-m`. **Never** pass `--no-gpg-sign`; if signing fails because 1Password is locked, stop and report it.
- **Every commit step is preceded by two checks:** `git branch --show-current` must print the branch the controller handed over (`feature/app-slots-probe-preinit-close` for Tasks 1–2, `feature/app-slots-start-stop` for Task 3), and `make check` must pass on the tree about to be committed. Implementers also run the targeted `go test` commands named in their task, plus `gofmt -w` on the files they touched (the code blocks below are written for readability, not for gofmt's alignment). **Implementers do not run `make mutate` or push** — the controller runs the full gate set (Task 4, and its PR3a counterpart after Task 2) before each push.
- **No `//nolint`.** If a linter fires, fix the code. Note `unused` is active (golangci-lint v2 standard set), so a helper this PR orphans must be deleted in the same task that orphans it.
- **Branch:** stay on the branch the controller hands over. Never switch branches. Never push to `main`.

---

## Decisions taken up front

These five go slightly beyond a mechanical extraction. They are settled here so no executor re-litigates them mid-task.

### 1. The streams probe stays withheld until its manager exists — no visible `/ready` change

`probe()` returns `(probeDescription, bool)`. The three classic slots always return `true` (a nil manager yields `disabledProbe`, exactly as today). The streams slot returns `false` while `App.streamsManager` is nil.

**Why:** ADR-066 rule 5 renders `<name>` and `<name>_stats` for *every registered kind*. A streams slot that registered a `disabled` description at build time would add `"streams": "disabled"` and `"streams_stats": {"status":"disabled"}` to the 200 body of **every** service in the fleet, including the overwhelming majority that never declared a stream. That is a visible body change requiring a migration atom, bought for nothing — the spec's own readiness decision 5 says "streams keeps registering at runtime until PR5 folds it into a slot". `App.prepareRuntime` re-collects the probe set after the start phase, so the streams probe appears exactly when it appears today: after a successful `streams.Manager.Start`. `TestReadyCheckOmitsStreamsWhenNoneDeclared` keeps passing untouched.

### 2. The single registration order is adopted for `start`, and the resulting reordering is accepted

Today `prepareRuntime` runs: AMQP consumers → streams `Start` → pre-warm(database) → pre-warm(messaging).
Under one registration order it runs: pre-warm(database) → [AMQP consumers → pre-warm(messaging)] → cache (nothing) → streams `Start`.

Two steps move earlier: the database pre-warm now precedes the AMQP consumer bootstrap and the streams start, and the messaging pre-warm now precedes the streams start.

**Adopted, with no atom line.** The argument from the code:

- **No dependency is violated.** The database pre-warm leases the `""` key on `DbManager` and releases it; it reads nothing messaging or streams produces. `EnsureConsumers` reads nothing the database pre-warm produces. `streams.Manager` builds its own `Environment` from `messaging.streams.uri` and shares no state with `messaging.Manager` or `DbManager`.
- **Nothing that was fatal becomes non-fatal, or vice versa.** The consumer bootstrap keeps the #907 fail-vs-warn grading; the streams start keeps aborting startup; both pre-warms stay advisory (WARN, never fatal).
- **The only cost is worst-case time-to-fail.** A service destined to abort on streams now first pays a database dial and a bounded publisher-readiness wait (`messaging.reconnect.readytimeout`, default 5s). Bounded, one-off, and paid only on a startup that was going to fail anyway.
- **Nothing in a documented contract moves:** no `/ready` key, no HTTP status, no config key, no exported symbol. Only the relative order of INFO/DEBUG startup log lines changes, and no test or wiki page asserts a cross-phase log order.

### 3. The `close` phase is expressed as `closer() (namedCloser, bool)`, not `close() error`

App's close phase already runs through the FIFO `App.closers` registry that `shutdownClosers` walks after module `Shutdown` (ADR-029). A slot handing over its *named closer* preserves that registry, its FIFO order, its `"Closing %d remaining resources"` count and its per-resource `"<Name> closed successfully"` INFO exactly. A `close()` method would either duplicate the registry or force every slot to register a no-op closer — which changes that count and adds a spurious `"Streams manager closed successfully"` to every service that never declared a stream. `ok == false` means "this kind has nothing to close yet".

### 4. `streams_setup.go` is **not** folded in this PR — PR5 owns it

PR3 moves only the two registration lines out of `prepareStreamConsumers` (the closer and the probe append); the construction, the three asserts, the plaintext-URI WARN and `shutdownStreamConsumers` stay where they are and are called from `streamsSlot.start` / `streamsSlot.stop`. Folding the 137-line body plus its 276-line test file now would add ~400 changed LoC to a PR already near the threshold, for no behavioural gain — and spec decision 7 slices it as PR5 precisely so the streams fold is reviewable on its own.

### 5. Two Debug lines can now fire where they could not before — accepted, not atom-worthy

Today the whole pre-warm pass is gated on `!multitenant && (dbManager != nil || messagingManager != nil)`, so a single-tenant service with **neither** manager emits neither `"Skipping single-tenant database pre-warming: manager unavailable"` nor its messaging twin. Per-slot gating emits both. They are DEBUG, below the framework's default `info` level outside development, and no test asserts their absence.

---

## File Structure

| File | Change | Responsibility after this PR |
| --- | --- | --- |
| `app/slot.go` | **create** (~210 lines) | The `resourceSlot` interface, the four per-kind structs and every per-kind lifecycle body; `startupContext` moves here from `app_builder.go` (its only callers are now slots). |
| `app/slot_test.go` | **create** (~330 lines) | The slot-set table (name/probe/closer coverage), the per-kind pre-init and start/stop behaviour tests, and the stub-slot order/aggregation tests. |
| `app/app.go` | modify | `App.slots` field; `installSlots`/`collectProbes`/`registerSlotCloser`/`registerSlotClosers`/`multiTenant` replace `createHealthProbes` and `probeInputs`. |
| `app/app_builder.go` | modify | `CreateApp` installs the slots; `performPreInitialization` iterates them; `preInitDatabase`, `preInitMessaging`, `preInitCache`, `preInitFatalComponent` and `startupContext` are removed from this file; `CreateHealthProbes`/`RegisterClosers` become loops. |
| `app/lifecycle.go` | modify | `prepareRuntime` runs `startSlots` and re-collects probes; `Shutdown` runs `stopSlots`. |
| `app/prewarm.go` | modify | `preWarmSingleTenant`, `attemptDatabasePreWarm`, `attemptMessagingPreWarm` delete (their bodies become the two slots' `start`); `preWarmDatabase`, `preWarmMessaging`, `publisherReadinessTimeout`, `awaitPublisherReady` survive with updated doc comments. |
| `app/streams_setup.go` | modify (−2 lines) | `prepareStreamConsumers` stops registering the closer and appending the probe. |
| `app/app_test.go` | modify | Fixture installs slots; `rebuildClosersAndHealth` becomes `rebuildLifecycle` and stops re-implementing the two Builder steps. |
| `app/app_builder_test.go` | modify | `TestPreInitCacheSkipsAbsentCache` moves to `slot_test.go`; the two `performPreInitialization` deadline tests are untouched. |
| `app/lifecycle_test.go` | modify | `newLifecycleCheckAppWithLogger` installs slots; `createHealthProbes` call sites become `installSlots` + `collectProbes`. |
| `app/prewarm_test.go` | modify | `TestPreWarmSingleTenant*` retarget at `messagingSlot.start`. |
| `app/readiness_test.go`, `app/debug_health_test.go`, `app/streams_setup_test.go` | modify | `createHealthProbes(probeInputs{…})` call sites retargeted; the streams probe/closer assertions move to `slot_test.go`. |

---

## Task 1: `resourceSlot`, the four slots, and the probe + close walks

**Files:**

- Create: `app/slot.go`, `app/slot_test.go`
- Modify: `app/app.go` (`App` struct ~line 63-92, `probeInputs`/`createHealthProbes` ~line 94-108), `app/app_builder.go` (`CreateApp`, `CreateHealthProbes`, `RegisterClosers`), `app/streams_setup.go` (drop 2 registration lines)
- Modify (tests): `app/app_test.go` (fixture), `app/readiness_test.go`, `app/debug_health_test.go`, `app/lifecycle_test.go`, `app/streams_setup_test.go`

**Interfaces:**

- Produces:
  - `type resourceSlot interface { name() string; probe() (probeDescription, bool); preInit(ctx context.Context) error; preInitFatal() bool; start(ctx context.Context) (advisory, fatal error); stop(ctx context.Context); closer() (namedCloser, bool) }` — the full interface is declared in this task; Task 1 implements `name`, `probe` and `closer` for real and leaves `preInit`/`start`/`stop` as thin delegates to the code that still lives in `app_builder.go` / `lifecycle.go`, which Tasks 2 and 3 move in.
  - `type slotInputs struct { cacheAbsent bool }`
  - `func (a *App) installSlots(inputs slotInputs)` — builds `a.slots` in the order database, messaging, cache, streams.
  - `func (a *App) collectProbes() []Prober`
  - `func (a *App) registerSlotCloser(s resourceSlot)` and `func (a *App) registerSlotClosers()`
  - `func (a *App) multiTenant() bool`
- Consumes: `probeDescription`, `databaseProbe`, `messagingProbe`, `cacheProbe`, `streamsProbe`, `disabledProbe` (all `app/readiness.go`); `namedCloser` (`app/internal_types.go`); `rootCacheAbsent` (`app/bootstrap.go`); `componentDatabase`/`componentMessaging`/`componentCache`/`componentStreams` (`app/app.go`).

- [ ] **Step 1: Write the failing tests**

Create `app/slot_test.go` with exactly this content.

```go
package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	databaseCloserName  = "database manager"
	messagingCloserName = "messaging manager"
	cacheCloserName     = "cache manager"
	streamsCloserName   = "streams manager"
)

// newSlotTestApp builds the App the slot walks read: real managers behind mock connectors,
// so probes and closers see the same pointers production hands them. Managers are attached
// only when asked for, because "absent kind" is half of what these walks decide.
func newSlotTestApp(t *testing.T, withDB, withMessaging bool) *App {
	t.Helper()

	cfg := defaultTestConfig()
	log := logger.New("error", false)
	source := config.NewTenantStore(cfg)

	a := &App{cfg: cfg, logger: log}

	if withDB {
		dbManager := database.NewDbManager(source, log,
			database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
			func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
				return dbtesting.NewTestDB(dbTypePostgres), nil
			})
		t.Cleanup(func() { assert.NoError(t, dbManager.Close()) })
		a.dbManager = dbManager
	}

	if withMessaging {
		client := testmocks.NewMockAMQPClient()
		client.ExpectClose(nil)
		messagingManager := messaging.NewMessagingManager(source, log,
			messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
			func(string, logger.Logger) messaging.AMQPClient { return client })
		t.Cleanup(func() { assert.NoError(t, messagingManager.Close()) })
		a.messagingManager = messagingManager
	}

	a.installSlots(slotInputs{})
	return a
}

// slotNames reads the installed slot list back as its kind names.
func slotNames(a *App) []string {
	names := make([]string, 0, len(a.slots))
	for _, s := range a.slots {
		names = append(names, s.name())
	}
	return names
}

// probeNames runs every collected probe and reports the component name each reported.
func probeNames(t *testing.T, probes []Prober) []string {
	t.Helper()
	names := make([]string, 0, len(probes))
	for _, p := range probes {
		names = append(names, p.Run(context.Background()).Name)
	}
	return names
}

// closerNames reads the registered close list back in FIFO order.
func closerNames(a *App) []string {
	names := make([]string, 0, len(a.closers))
	for _, c := range a.closers {
		names = append(names, c.name)
	}
	return names
}

// TestInstallSlotsCoversEveryKindInRegistrationOrder is the completeness pin: one slot per
// kind, in the one order every phase walks (spec decision 8).
func TestInstallSlotsCoversEveryKindInRegistrationOrder(t *testing.T) {
	a := newSlotTestApp(t, false, false)

	assert.Equal(t,
		[]string{componentDatabase, componentMessaging, componentCache, componentStreams},
		slotNames(a))
}

// TestSlotWalksCoverEveryKind is the table the spec asks for: for each kind, whether it
// contributes a probe at build time and whether it contributes a closer, with and without
// its manager. It is one table rather than four tests because the point being pinned is
// that the SET is uniform — a fifth kind added tomorrow lands in it.
func TestSlotWalksCoverEveryKind(t *testing.T) {
	cases := []struct {
		name        string
		withDB      bool
		withMsg     bool
		wantProbes  []string
		wantClosers []string
	}{
		{
			name:        "no_managers_probes_three_disabled_kinds_and_closes_nothing",
			wantProbes:  []string{componentDatabase, componentMessaging, componentCache},
			wantClosers: []string{},
		},
		{
			name:        "database_only",
			withDB:      true,
			wantProbes:  []string{componentDatabase, componentMessaging, componentCache},
			wantClosers: []string{databaseCloserName},
		},
		{
			name:        "database_and_messaging",
			withDB:      true,
			withMsg:     true,
			wantProbes:  []string{componentDatabase, componentMessaging, componentCache},
			wantClosers: []string{databaseCloserName, messagingCloserName},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newSlotTestApp(t, tc.withDB, tc.withMsg)

			assert.Equal(t, tc.wantProbes, probeNames(t, a.collectProbes()))

			a.registerSlotClosers()
			assert.Equal(t, tc.wantClosers, closerNames(a))
		})
	}
}

// TestCacheSlotContributesItsProbeAndCloser pins the third classic kind, which
// newSlotTestApp cannot wire without a Redis stand-in.
func TestCacheSlotContributesItsProbeAndCloser(t *testing.T) {
	a := newSlotTestApp(t, false, false)
	a.cacheManager = createTestCacheManager(t)

	assert.Equal(t,
		[]string{componentDatabase, componentMessaging, componentCache},
		probeNames(t, a.collectProbes()))

	a.registerSlotClosers()
	assert.Equal(t, []string{cacheCloserName}, closerNames(a))
}

// TestCollectProbesWithholdsStreamsUntilItsManagerExists pins the one kind whose
// description is withheld: registering a disabled streams description at build time would
// add "streams" and "streams_stats" to every service's /ready body (ADR-066 rule 5), which
// nothing asked for. See the plan's decision 1.
func TestCollectProbesWithholdsStreamsUntilItsManagerExists(t *testing.T) {
	a := newSlotTestApp(t, false, false)
	require.Len(t, a.collectProbes(), 3, "a streams-free service registers three kinds")

	a.streamsManager = streams.NewManager(streams.ManagerOptions{
		URI:    unreachableStreamURI,
		Logger: a.logger,
	})
	t.Cleanup(func() { _ = a.streamsManager.Close() })

	probes := a.collectProbes()
	require.Len(t, probes, 4)
	assert.Equal(t,
		[]string{componentDatabase, componentMessaging, componentCache, componentStreams},
		probeNames(t, probes),
		"streams registers last, exactly where the runtime append put it")
}

// TestCacheSlotTakesAbsenceFromItsInputs pins that the cache description's absence arm is
// driven by the Builder's verdict rather than by state stored on App: absence needs
// Options, which only the Builder holds, so a second copy on App could drift from it. The
// connector always fails, so the two arms are told apart by whether it was reached at all.
func TestCacheSlotTakesAbsenceFromItsInputs(t *testing.T) {
	newApp := func(absent bool) *App {
		a := &App{
			cfg:    defaultTestConfig(),
			logger: logger.New("error", false),
			cacheManager: createTestCacheManagerWithGetError(t,
				errNeverReachTheConnector),
		}
		a.installSlots(slotInputs{cacheAbsent: absent})
		return a
	}

	absent := newApp(true).collectProbes()[2].Run(context.Background())
	present := newApp(false).collectProbes()[2].Run(context.Background())

	assert.Equal(t, notConfiguredStatus, absent.Status, "an absent cache is judged without leasing")
	assert.Equal(t, unhealthyStatus, present.Status, "a present cache leases and reports the connector's failure")
}

// TestSlotProbesTrackLiveManagers pins that the slots read App's manager fields at probe
// time rather than snapshotting them at install time: the fixtures below swap a manager out
// after installSlots and expect the next collection to follow.
func TestSlotProbesTrackLiveManagers(t *testing.T) {
	a := newSlotTestApp(t, true, true)
	require.Equal(t, healthyStatus, a.collectProbes()[1].Run(context.Background()).Status)

	a.messagingManager = nil

	assert.Equal(t, disabledStatus, a.collectProbes()[1].Run(context.Background()).Status)
}
```

Add this sentinel beside the other package-level test errors in `app/slot_test.go`, immediately after the `const` block:

```go
// errNeverReachTheConnector fails any lease the absent arm should never attempt.
var errNeverReachTheConnector = errors.New("the absent arm must never reach the connector")
```

and add `"errors"` to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./app/ -run 'TestInstallSlots|TestSlotWalks|TestCacheSlot|TestCollectProbesWithholds|TestSlotProbesTrack' -count=1`
Expected: FAIL — compile error, `a.installSlots undefined (type *App has no field or method installSlots)`, plus the same for `slotInputs`, `a.slots`, `a.collectProbes`, `a.registerSlotClosers`.

- [ ] **Step 3: Create `app/slot.go`**

```go
package app

import (
	"context"
	"time"
)

// A slot is the framework-side module that owns one resource kind's whole application
// lifecycle — probe, pre-init, start, stop, close — so that adding a kind is one slot, not
// an edit in every place that enumerates kinds (CONTEXT.md, ADR-067).
//
// ADR-045: the interface lives in app/ and names only what app calls. The managers behind
// it (database.DbManager, messaging.Manager, cache.CacheManager, streams.Manager) know
// nothing about it.
type resourceSlot interface {
	// name is the kind's fixed component identifier. SECURITY: the probe description carries
	// this same string onto the unauthenticated /ready body, so it is never a tenant, host or
	// database name.
	name() string

	// probe returns the kind's readiness description and whether it is registered at all.
	// Only the streams slot withholds one — see streamsSlot.probe.
	probe() (probeDescription, bool)

	// preInit establishes the kind's fixed-"" -key connection during Builder construction.
	// It returns the raw failure; preInitFatal decides what that costs.
	preInit(ctx context.Context) error

	// preInitFatal reports whether a preInit failure aborts startup.
	preInitFatal() bool

	// start brings the kind up in prepareRuntime. A non-nil fatal aborts startup at once; a
	// non-nil advisory is aggregated into the single pre-warm WARN and never fails startup.
	start(ctx context.Context) (advisory, fatal error)

	// stop halts the kind's inbound work before module Shutdown (ADR-029). It never closes
	// connections — that is the close phase, which runs after modules are torn down.
	stop(ctx context.Context)

	// closer hands over the resource the close phase must Close. ok is false when the kind
	// has nothing to close yet, which is how an unconfigured kind and a streams manager that
	// has not started stay out of the FIFO close list.
	closer() (namedCloser, bool)
}

var (
	_ resourceSlot = (*databaseSlot)(nil)
	_ resourceSlot = (*messagingSlot)(nil)
	_ resourceSlot = (*cacheSlot)(nil)
	_ resourceSlot = (*streamsSlot)(nil)
)

// slotInputs carries the verdicts a slot cannot reach from App alone. Only one qualifies:
// the cache's absence under the fixed "" key reads Options (rootCacheAbsent), which the
// Builder holds and App does not.
type slotInputs struct {
	cacheAbsent bool
}

// installSlots builds the one slot list every lifecycle phase walks, in the one
// registration order: database → messaging → cache → streams. Close stays FIFO over the
// same order. Each slot holds the App rather than a snapshot of its managers, so a manager
// swapped in later (the streams manager, which only exists after start) is seen by the next
// walk without rebuilding the list.
func (a *App) installSlots(inputs slotInputs) {
	a.slots = []resourceSlot{
		&databaseSlot{app: a},
		&messagingSlot{app: a},
		&cacheSlot{app: a, absent: inputs.cacheAbsent},
		&streamsSlot{app: a},
	}
}

// collectProbes is the readiness walk: every slot that has a description to register, in
// registration order.
func (a *App) collectProbes() []Prober {
	probes := make([]Prober, 0, len(a.slots))
	for _, s := range a.slots {
		if description, ok := s.probe(); ok {
			probes = append(probes, description)
		}
	}
	return probes
}

// registerSlotCloser appends one slot's closer to the FIFO close list, if it has one.
func (a *App) registerSlotCloser(s resourceSlot) {
	if c, ok := s.closer(); ok {
		a.registerCloser(c.name, c.closer)
	}
}

// registerSlotClosers is the close walk: every slot's closer, in registration order.
func (a *App) registerSlotClosers() {
	for _, s := range a.slots {
		a.registerSlotCloser(s)
	}
}

// startupContext derives one component's pre-init context from parent. A non-positive budget means
// "no explicit budget", NOT "already expired": WithConfig's config.Validate call resolves the
// three-level fallback (config.applyStartupDefaults) for every config reaching NewWithConfig, but a
// Builder assembled without WithConfig can still carry a zero-valued Startup, and
// context.WithTimeout(parent, 0) would hand every component a context that is dead on arrival.
func startupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// databaseSlot owns the database kind.
type databaseSlot struct{ app *App }

func (s *databaseSlot) name() string { return componentDatabase }

func (s *databaseSlot) probe() (probeDescription, bool) {
	return databaseProbe(s.app.dbManager, s.app.multiTenant()), true
}

func (s *databaseSlot) preInitFatal() bool { return true }

func (s *databaseSlot) stop(context.Context) {}

func (s *databaseSlot) closer() (namedCloser, bool) {
	if s.app.dbManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "database manager", closer: s.app.dbManager}, true
}

// messagingSlot owns the AMQP kind.
type messagingSlot struct{ app *App }

func (s *messagingSlot) name() string { return componentMessaging }

func (s *messagingSlot) probe() (probeDescription, bool) {
	return messagingProbe(s.app.messagingManager, s.app.multiTenant()), true
}

func (s *messagingSlot) preInitFatal() bool { return true }

func (s *messagingSlot) stop(context.Context) { s.app.shutdownConsumers() }

func (s *messagingSlot) closer() (namedCloser, bool) {
	if s.app.messagingManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "messaging manager", closer: s.app.messagingManager}, true
}

// cacheSlot owns the cache kind.
type cacheSlot struct {
	app *App
	// absent is the Builder's rootCacheAbsent verdict, captured once at installSlots because
	// it reads Options, which App does not hold.
	absent bool
}

func (s *cacheSlot) name() string { return componentCache }

func (s *cacheSlot) probe() (probeDescription, bool) {
	return cacheProbe(s.app.cacheManager, s.app.cfg.IsCacheCritical(), s.absent, s.app.multiTenant()), true
}

func (s *cacheSlot) preInitFatal() bool { return false }

func (s *cacheSlot) start(context.Context) (advisory, fatal error) { return nil, nil }

func (s *cacheSlot) stop(context.Context) {}

func (s *cacheSlot) closer() (namedCloser, bool) {
	if s.app.cacheManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "cache manager", closer: s.app.cacheManager}, true
}

// streamsSlot owns the native stream-protocol kind. Its manager does not exist until start.
type streamsSlot struct{ app *App }

func (s *streamsSlot) name() string { return componentStreams }

// probe withholds a description until the manager exists. Registering a disabled one at
// build time would add "streams" and "streams_stats" to the /ready body of every service in
// the fleet, the overwhelming majority of which never declared a stream (ADR-066 rule 5
// renders every registered kind). prepareRuntime re-collects after the start phase, so the
// description appears exactly where the runtime registration put it.
func (s *streamsSlot) probe() (probeDescription, bool) {
	if s.app.streamsManager == nil {
		return probeDescription{}, false
	}
	return streamsProbe(s.app.streamsManager), true
}

func (s *streamsSlot) preInit(context.Context) error { return nil }

func (s *streamsSlot) preInitFatal() bool { return false }

func (s *streamsSlot) stop(context.Context) { s.app.shutdownStreamConsumers() }

func (s *streamsSlot) closer() (namedCloser, bool) {
	if s.app.streamsManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "streams manager", closer: s.app.streamsManager}, true
}
```

Task 1 leaves three interface methods still delegating to the code that has not moved yet. Add them at the end of `app/slot.go`, marked so Tasks 2 and 3 know what to replace:

```go
// The three phases below still delegate to the code that owns them today. Task 2 moves the
// pre-init bodies here; Task 3 moves the start bodies here.

func (s *databaseSlot) preInit(ctx context.Context) error {
	return s.app.builderPreInitDatabase(ctx)
}

func (s *messagingSlot) preInit(ctx context.Context) error {
	return s.app.builderPreInitMessaging(ctx)
}

func (s *cacheSlot) preInit(ctx context.Context) error {
	return s.app.builderPreInitCache(ctx, s.absent)
}

func (s *databaseSlot) start(context.Context) (advisory, fatal error) { return nil, nil }

func (s *messagingSlot) start(context.Context) (advisory, fatal error) { return nil, nil }

func (s *streamsSlot) start(context.Context) (advisory, fatal error) { return nil, nil }
```

**Stop.** Those three `builderPreInit*` names do not exist and inventing them would be dead scaffolding deleted one task later. Delete the block above and use this instead — Task 1's slots simply have no pre-init or start yet, because nothing calls them yet:

```go
// preInit and start are wired in Tasks 2 and 3; until then nothing calls them, because
// performPreInitialization and prepareRuntime still hold their own per-kind call sites.

func (s *databaseSlot) preInit(context.Context) error { return nil }

func (s *messagingSlot) preInit(context.Context) error { return nil }

func (s *cacheSlot) preInit(context.Context) error { return nil }

func (s *databaseSlot) start(context.Context) (advisory, fatal error) { return nil, nil }

func (s *messagingSlot) start(context.Context) (advisory, fatal error) { return nil, nil }

func (s *streamsSlot) start(context.Context) (advisory, fatal error) { return nil, nil }
```

- [ ] **Step 4: Wire `App` to the slots**

In `app/app.go`, add the field to the `App` struct, immediately after `healthProbes`:

```go
	closers      []namedCloser
	healthProbes []Prober

	// slots is the one per-kind lifecycle list every phase walks, in the fixed order
	// database → messaging → cache → streams (installSlots, ADR-067).
	slots []resourceSlot
```

Delete `probeInputs` and `createHealthProbes` (`app/app.go`, the block starting `// probeInputs carries the verdicts createHealthProbes cannot reach` and ending with the closing brace of `createHealthProbes`) and put this in their place:

```go
// multiTenant reports whether this deployment resolves its resources per tenant. Nil-guarded
// because a directly-constructed App may carry no config.
func (a *App) multiTenant() bool {
	return a.cfg != nil && a.cfg.Multitenant.Enabled
}
```

`installSlots`, `collectProbes`, `registerSlotCloser` and `registerSlotClosers` live in `app/slot.go` (Step 3) — do not duplicate them here.

- [ ] **Step 5: Make the Builder steps iterate the slots**

In `app/app_builder.go`, `CreateApp`: append one line after the `b.app = &App{…}` literal, before `return b`.

```go
	// Slots are installed here, before ConfigureRuntimeHelpers runs pre-initialization over
	// them. cacheAbsent is the one verdict App cannot re-derive: it reads Options.
	b.app.installSlots(slotInputs{cacheAbsent: rootCacheAbsent(b.cfg, b.opts)})

	return b
```

In `app/app_builder.go`, `CreateHealthProbes`: replace the assignment line

```go
	b.app.healthProbes = b.app.createHealthProbes(probeInputs{cacheAbsent: rootCacheAbsent(b.cfg, b.opts)})
```

with

```go
	b.app.healthProbes = b.app.collectProbes()
```

In `app/app_builder.go`, `RegisterClosers`: replace the three explicit registrations with the walk.

```go
	// Register closers with explicit nil checks to avoid typed nil interface issues
	if b.app.dbManager != nil {
		b.app.registerCloser("database manager", b.app.dbManager)
	}
	if b.app.messagingManager != nil {
		b.app.registerCloser("messaging manager", b.app.messagingManager)
	}
	if b.app.cacheManager != nil {
		b.app.registerCloser("cache manager", b.app.cacheManager)
	}
	return b
```

becomes

```go
	// Each slot decides whether it has anything to close; the nil checks that avoided typed
	// nil interfaces now live in the slots' closer methods.
	b.app.registerSlotClosers()
	return b
```

- [ ] **Step 6: Move the streams registration out of `prepareStreamConsumers`**

In `app/streams_setup.go`, delete the closer registration and the probe append, and shorten the doc comment's runtime-registration paragraph.

```go
	a.streamsManager = mgr
	a.registerCloser("streams manager", mgr)
	a.healthProbes = append(a.healthProbes, streamsProbe(mgr))

	return nil
```

becomes

```go
	a.streamsManager = mgr

	return nil
```

and the doc comment paragraph

```go
// Everything happens at RUNTIME on purpose: the manager does not exist while the
// builder runs, so its readiness probe and its closer are both registered here
// rather than in createHealthProbes or Builder.RegisterClosers, which are
// snapshotted before prepareRuntime runs. This is safe because prepareRuntime is
// single-threaded and completes before the server starts serving /ready.
```

becomes

```go
// Everything happens at RUNTIME on purpose: the manager does not exist while the
// builder runs. The streams slot owns its probe and its closer (app/slot.go) and
// registers both once this function has produced the manager.
```

- [ ] **Step 7: Update the test call sites**

`app/app_test.go` — in `newTestAppFixture`, replace

```go
	fixture.rebuildClosersAndHealth()
	fixture.server.RegisterReadyHandler(fixture.app.readyCheck)
```

with

```go
	fixture.app.installSlots(slotInputs{})
	fixture.rebuildLifecycle()
	fixture.server.RegisterReadyHandler(fixture.app.readyCheck)
```

and replace the whole `rebuildClosersAndHealth` method with

```go
// rebuildLifecycle re-runs the two Builder steps that snapshot the slot walks, after a test
// has swapped a manager in or out. It calls exactly what Builder.CreateHealthProbes and
// Builder.RegisterClosers call, so the fixture cannot drift from production wiring.
func (f *testAppFixture) rebuildLifecycle() {
	f.app.healthProbes = f.app.collectProbes()
	f.app.closers = nil
	f.app.registerSlotClosers()
}
```

Then rename every `f.rebuildClosersAndHealth()` call to `f.rebuildLifecycle()` — nine sites in `app/app_test.go` (lines ~930, 948, 973, 993, 1019, 1036, 1056, 1071, 1090). Verify none remain:

```bash
git grep -n "rebuildClosersAndHealth" -- app/ && echo "STILL PRESENT — fix before continuing"
```

Every remaining `createHealthProbes(probeInputs{…})` call site becomes two lines. Apply this transformation at each:

```go
	app.healthProbes = app.createHealthProbes(probeInputs{})
```

becomes

```go
	app.installSlots(slotInputs{})
	app.healthProbes = app.collectProbes()
```

The sites are: `app/debug_health_test.go:242`, `app/debug_health_test.go:310`, `app/lifecycle_test.go:846`, `app/lifecycle_test.go:904`, `app/lifecycle_test.go:964`, `app/lifecycle_test.go:979`. In each case the local variable is named `app`, so the two lines above transcribe verbatim.

In `app/readiness_test.go`, replace `TestCreateHealthProbesAlwaysDescribesTheThreeClassicKinds` and `TestCreateHealthProbesTakesAbsenceFromItsInputs` — both now live in `app/slot_test.go` as `TestSlotWalksCoverEveryKind` and `TestCacheSlotTakesAbsenceFromItsInputs`. Delete both functions outright; do not leave a shim.

In `app/streams_setup_test.go`, the three probe assertions no longer describe what `prepareStreamConsumers` does. Replace

```go
	assert.Empty(t, a.healthProbes, "a streams-free service keeps its probe list unchanged")
	assert.Empty(t, a.closers)
```

(at line ~79) with

```go
	assert.Empty(t, a.closers)
```

and delete the bare `assert.Empty(t, a.healthProbes)` lines at ~148 and ~230, keeping the `assert.Empty(t, a.closers)` and `assert.Nil(t, a.streamsManager)` assertions beside them. The probe half is now covered by `TestCollectProbesWithholdsStreamsUntilItsManagerExists`; the closer half moves in Task 3.

- [ ] **Step 8: Run the new tests to verify they pass**

Run: `go test ./app/ -run 'TestInstallSlots|TestSlotWalks|TestCacheSlot|TestCollectProbesWithholds|TestSlotProbesTrack' -count=1 -race`
Expected: PASS (`ok github.com/gaborage/go-bricks/app`)

- [ ] **Step 9: Run the whole package to verify nothing regressed**

Run: `go test ./app/ -count=1 -race`
Expected: PASS. `TestReadyCheckOmitsStreamsWhenNoneDeclared`, `TestAppBuilderCreateHealthProbesAppliesCacheCritical` and every `/ready` body case in `app/app_test.go` must be green without their assertions changing — this PR does not move the body.

- [ ] **Step 10: Format and commit**

```bash
gofmt -w app/slot.go app/slot_test.go app/app.go app/app_builder.go app/streams_setup.go \
  app/app_test.go app/readiness_test.go app/debug_health_test.go app/lifecycle_test.go \
  app/streams_setup_test.go
```

```bash
cat > /tmp/slot-commit-1.txt <<'EOF'
refactor(app): give each resource kind a lifecycle slot

Every resource kind's application lifecycle facts were hand-enumerated at
eight call sites, so adding the streams kind touched six files and still
needed a runtime-registration exception. Introduce the unexported
resourceSlot interface and four per-kind slots holding the App, installed
once by Builder.CreateApp in the one registration order database ->
messaging -> cache -> streams.

The probe and close walks move first: Builder.CreateHealthProbes calls
App.collectProbes and Builder.RegisterClosers calls App.registerSlotClosers,
both iterating the slot list instead of naming the kinds. App.createHealthProbes
and probeInputs are replaced by installSlots/collectProbes plus slotInputs,
which carries the one verdict App cannot re-derive (rootCacheAbsent reads
Options). prepareStreamConsumers stops registering the streams closer and
appending the streams probe; the streams slot owns both.

The streams slot deliberately withholds its probe description while its
manager is nil. ADR-066 rule 5 renders <name> and <name>_stats for every
registered kind, so a disabled description at build time would add
"streams": "disabled" to the /ready body of every service in the fleet.
The body is byte-identical after this change.

The test fixture's rebuildClosersAndHealth, which re-implemented both
Builder steps, becomes rebuildLifecycle and calls exactly what the Builder
calls, so it can no longer drift from production wiring.
EOF
git add app/slot.go app/slot_test.go app/app.go app/app_builder.go app/streams_setup.go \
  app/app_test.go app/readiness_test.go app/debug_health_test.go app/lifecycle_test.go \
  app/streams_setup_test.go
git commit -F /tmp/slot-commit-1.txt
```

---

## Task 2: `preInit` moves into the slots and `performPreInitialization` iterates

**Files:**

- Modify: `app/slot.go` (replace the three placeholder `preInit` bodies), `app/app_builder.go` (`performPreInitialization`; delete `preInitDatabase`, `preInitMessaging`, `preInitCache`, `preInitFatalComponent`, `startupContext`)
- Modify (tests): `app/slot_test.go` (add), `app/app_builder_test.go` (move `TestPreInitCacheSkipsAbsentCache` out)

**Interfaces:**

- Consumes from Task 1: `resourceSlot.preInit`, `resourceSlot.preInitFatal`, `App.slots`, `startupContext` (now in `app/slot.go`).
- Produces: the four real `preInit` bodies. `Builder.performPreInitialization` keeps its name and signature (`func (b *Builder) performPreInitialization()`); Task 3 does not touch it.

**Behaviour that must survive byte-for-byte:** the Debug line `"Performing pre-initialization for static single-tenant mode"`; the per-component Debug lines `"Skipping %s pre-initialization: not configured"` and `"Pre-initialized %s connection"` (cache's variants read `"Skipping cache pre-initialization: not configured"` / `"Pre-initialized cache connection"` — the same strings the `%s` form produces); the WARN `"Failed to pre-initialize cache connection (non-fatal)"`; the fatal error `"%s connection failed during startup: %w"`; a fatal failure stopping the remaining components; and each component deriving its own budget from `app.startup.{database,messaging,cache}` off one shared parent.

- [ ] **Step 1: Write the failing tests**

Append to `app/slot_test.go`:

```go
// TestSlotPreInitFatality pins the classification the spec fixes: database and messaging
// abort startup, cache is best-effort, streams has no pre-init at all.
func TestSlotPreInitFatality(t *testing.T) {
	a := newSlotTestApp(t, false, false)
	require.Len(t, a.slots, 4)

	assert.True(t, a.slots[0].preInitFatal(), "a misconfigured database must fail startup")
	assert.True(t, a.slots[1].preInitFatal(), "a misconfigured broker must fail startup")
	assert.False(t, a.slots[2].preInitFatal(), "an unreachable cache is a runtime condition")
	assert.False(t, a.slots[3].preInitFatal(), "streams has no pre-init")
}

// TestDatabaseSlotPreInitSkipsUnconfiguredKind pins the pre-check: an unconfigured database
// is skipped without ever leasing, so the pool's error counter starts at a true zero.
func TestDatabaseSlotPreInitSkipsUnconfiguredKind(t *testing.T) {
	a := newSlotTestApp(t, true, false)
	a.cfg.Database = config.DatabaseConfig{} // nothing configured

	require.NoError(t, a.slots[0].preInit(context.Background()))
	assert.Equal(t, 0, statsInt(t, a.dbManager.Stats(), statsActiveConnectionsKey),
		"the unconfigured arm must never open a connection")
}

// TestDatabaseSlotPreInitReportsLeaseFailure pins that the raw failure reaches the caller,
// which is what performPreInitialization turns into the fatal startup error.
func TestDatabaseSlotPreInitReportsLeaseFailure(t *testing.T) {
	log := logger.New("error", false)
	cfg := defaultTestConfig()
	dbManager := database.NewDbManager(staticDBConfigProvider{err: errNeverReachTheConnector}, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return dbtesting.NewTestDB(dbTypePostgres), nil
		})
	t.Cleanup(func() { assert.NoError(t, dbManager.Close()) })

	a := &App{cfg: cfg, logger: log, dbManager: dbManager}
	a.installSlots(slotInputs{})

	err := a.slots[0].preInit(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, errNeverReachTheConnector)
}

// TestCacheSlotPreInitSkipsAbsentCache pins that the cache is never leased when the fixed ""
// key can never resolve (rootCacheAbsent), so the pool's errors counter starts at a true
// zero. Moved here from app_builder_test.go, where it drove Builder.preInitCache.
func TestCacheSlotPreInitSkipsAbsentCache(t *testing.T) {
	newApp := func(t *testing.T, absent bool, calls *atomic.Int32) *App {
		t.Helper()
		mgr := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
			calls.Add(1)
			return nil, config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host")
		})
		t.Cleanup(func() { assert.NoError(t, mgr.Close()) })

		a := &App{cfg: defaultTestConfig(), logger: logger.New("error", false), cacheManager: mgr}
		a.installSlots(slotInputs{cacheAbsent: absent})
		return a
	}

	t.Run("absent_skips_the_connector", func(t *testing.T) {
		var calls atomic.Int32
		a := newApp(t, true, &calls)

		require.NoError(t, a.slots[2].preInit(context.Background()))

		assert.Equal(t, int32(0), calls.Load(), "the connector must never be reached")
	})

	t.Run("present_reaches_the_connector", func(t *testing.T) {
		var calls atomic.Int32
		a := newApp(t, false, &calls)

		require.NoError(t, a.slots[2].preInit(context.Background()),
			"a not-configured lease is a silent skip, not a failure")

		assert.Equal(t, int32(1), calls.Load(), "an unexempt cache must still be probed")
	})
}

// TestCacheSlotPreInitSurfacesRealFailures pins the other cache arm: an error that is NOT
// config.IsNotConfigured reaches the caller, which turns it into the non-fatal WARN.
func TestCacheSlotPreInitSurfacesRealFailures(t *testing.T) {
	mgr := createTestCacheManagerWithGetError(t, errNeverReachTheConnector)
	t.Cleanup(func() { assert.NoError(t, mgr.Close()) })

	a := &App{cfg: defaultTestConfig(), logger: logger.New("error", false), cacheManager: mgr}
	a.installSlots(slotInputs{})

	assert.ErrorIs(t, a.slots[2].preInit(context.Background()), errNeverReachTheConnector)
}

// TestStreamsSlotPreInitIsANoop pins that the streams kind contributes nothing to the
// Builder's pre-initialization pass — its manager does not exist until start.
func TestStreamsSlotPreInitIsANoop(t *testing.T) {
	a := newSlotTestApp(t, false, false)

	assert.NoError(t, a.slots[3].preInit(context.Background()))
}
```

Add the helper `statsInt` at the bottom of `app/slot_test.go`:

```go
// statsInt reads one integer counter out of a manager's Stats map, whatever width the
// manager published it as.
func statsInt(t *testing.T, stats map[string]any, key string) int {
	t.Helper()
	switch v := stats[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	default:
		t.Fatalf("stats[%q] is %T, not an integer", key, stats[key])
		return 0
	}
}
```

Add `"sync/atomic"` and `"github.com/gaborage/go-bricks/cache"` to `app/slot_test.go`'s imports. `staticDBConfigProvider` already exists in `app/lifecycle_test.go` (same package); do not redeclare it.

Append to `app/app_builder_test.go` (the loop's own behaviour, which no slot test can cover):

```go
// TestPerformPreInitializationStopsAtTheFirstFatalKind pins that a fatal pre-init aborts
// the walk: the messaging and cache slots that follow the database must not be reached, and
// the error must carry the failing kind's name.
func TestPerformPreInitializationStopsAtTheFirstFatalKind(t *testing.T) {
	order := []string{}
	builder := &Builder{
		cfg:    defaultTestConfig(),
		logger: logger.New("error", false),
		app:    &App{cfg: defaultTestConfig(), logger: logger.New("error", false)},
	}
	builder.app.slots = []resourceSlot{
		&recordingSlot{kind: componentDatabase, order: &order, fatalPreInit: true, preInitErr: assert.AnError},
		&recordingSlot{kind: componentMessaging, order: &order},
		&recordingSlot{kind: componentCache, order: &order},
	}

	builder.performPreInitialization()

	require.Error(t, builder.err)
	assert.Contains(t, builder.err.Error(), "database connection failed during startup")
	assert.Equal(t, []string{"preinit:database"}, order,
		"a fatal pre-init must stop the walk before the next kind")
}

// TestPerformPreInitializationContinuesPastABestEffortKind is the other half: a non-fatal
// failure is logged and the walk carries on.
func TestPerformPreInitializationContinuesPastABestEffortKind(t *testing.T) {
	order := []string{}
	rec := &recLogger{}
	builder := &Builder{
		cfg:    defaultTestConfig(),
		logger: rec,
		app:    &App{cfg: defaultTestConfig(), logger: rec},
	}
	builder.app.slots = []resourceSlot{
		&recordingSlot{kind: componentCache, order: &order, preInitErr: assert.AnError},
		&recordingSlot{kind: componentStreams, order: &order},
	}

	builder.performPreInitialization()

	require.NoError(t, builder.err)
	assert.Equal(t, []string{"preinit:cache", "preinit:streams"}, order)
	event, emitted := loggedEvent(rec, "Failed to pre-initialize cache connection (non-fatal)")
	require.True(t, emitted, "a best-effort failure must still be visible")
	assert.Equal(t, "warn", event.level)
}
```

Add the stub slot to `app/slot_test.go` (Task 3 reuses it for the start and stop walks, so it carries all seven methods now):

```go
// recordingSlot is a resourceSlot stand-in that records which phase ran on which kind, so
// the walks can be pinned on order and short-circuiting without standing up four real
// managers. Every field defaults to "this phase succeeds and does nothing".
type recordingSlot struct {
	order        *[]string
	preInitErr   error
	startAdvice  error
	startFatal   error
	kind         string
	fatalPreInit bool
}

func (s *recordingSlot) record(phase string) { *s.order = append(*s.order, phase+":"+s.kind) }

func (s *recordingSlot) name() string { return s.kind }

func (s *recordingSlot) probe() (probeDescription, bool) { return probeDescription{}, false }

func (s *recordingSlot) preInit(context.Context) error {
	s.record("preinit")
	return s.preInitErr
}

func (s *recordingSlot) preInitFatal() bool { return s.fatalPreInit }

func (s *recordingSlot) start(context.Context) (advisory, fatal error) {
	s.record("start")
	return s.startAdvice, s.startFatal
}

func (s *recordingSlot) stop(context.Context) { s.record("stop") }

func (s *recordingSlot) closer() (namedCloser, bool) { return namedCloser{}, false }

var _ resourceSlot = (*recordingSlot)(nil)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./app/ -run 'TestSlotPreInitFatality|TestDatabaseSlotPreInit|TestCacheSlotPreInit|TestStreamsSlotPreInitIsANoop|TestPerformPreInitialization' -count=1`
Expected: FAIL — `TestSlotPreInitFatality` passes already (Task 1 wired `preInitFatal`), but `TestDatabaseSlotPreInitReportsLeaseFailure` fails with `Error "..." not found` / `Expected an error but got nil` because the placeholder `preInit` returns nil, and `TestPerformPreInitializationStopsAtTheFirstFatalKind` fails with `Expected an error but got nil` because the loop does not exist yet.

- [ ] **Step 3: Write the real `preInit` bodies**

In `app/slot.go`, delete the placeholder block

```go
// preInit and start are wired in Tasks 2 and 3; until then nothing calls them, because
// performPreInitialization and prepareRuntime still hold their own per-kind call sites.

func (s *databaseSlot) preInit(context.Context) error { return nil }

func (s *messagingSlot) preInit(context.Context) error { return nil }

func (s *cacheSlot) preInit(context.Context) error { return nil }
```

(leave the three `start` placeholders — Task 3 replaces them) and put each body beside its slot's other methods instead.

`databaseSlot`, after `preInitFatal`:

```go
// preInit leases the fixed "" key under app.startup.database to verify connectivity, then
// releases it. A failure is startup-fatal: a misconfigured backing store must not boot green.
func (s *databaseSlot) preInit(ctx context.Context) error {
	if s.app.dbManager == nil {
		return nil
	}
	if !config.IsDatabaseConfigured(&s.app.cfg.Database) {
		s.app.logger.Debug().Msgf("Skipping %s pre-initialization: not configured", componentDatabase)
		return nil
	}

	ctx, cancel := startupContext(ctx, s.app.cfg.App.Startup.Database)
	defer cancel()

	_, release, err := s.app.dbManager.Get(ctx, "")
	if err != nil {
		return err
	}
	release() // startup probe only verifies connectivity; release the lease immediately
	s.app.logger.Debug().Msgf("Pre-initialized %s connection", componentDatabase)
	return nil
}
```

`messagingSlot`, after `preInitFatal`:

```go
// preInit leases the fixed "" key's publisher under app.startup.messaging to verify
// connectivity, then releases it. Startup-fatal, for the same reason as the database.
func (s *messagingSlot) preInit(ctx context.Context) error {
	if s.app.messagingManager == nil {
		return nil
	}
	if !config.IsMessagingConfigured(&s.app.cfg.Messaging) {
		s.app.logger.Debug().Msgf("Skipping %s pre-initialization: not configured", componentMessaging)
		return nil
	}

	ctx, cancel := startupContext(ctx, s.app.cfg.App.Startup.Messaging)
	defer cancel()

	_, release, err := s.app.messagingManager.Publisher(ctx, "")
	if err != nil {
		return err
	}
	release() // startup probe only verifies connectivity; release the lease immediately
	s.app.logger.Debug().Msgf("Pre-initialized %s connection", componentMessaging)
	return nil
}
```

`cacheSlot`, after `preInitFatal`:

```go
// preInit leases the fixed "" key under app.startup.cache, unless that key can never
// resolve (absent). Best-effort: reaching the cache is a runtime concern, distinct from the
// manager-creation contract, which already failed closed at CreateCacheManager. A lease that
// reports not-configured is a silent skip, not a failure.
func (s *cacheSlot) preInit(ctx context.Context) error {
	if s.app.cacheManager == nil || s.absent {
		return nil
	}

	ctx, cancel := startupContext(ctx, s.app.cfg.App.Startup.Cache)
	defer cancel()

	_, release, err := s.app.cacheManager.Get(ctx, "")
	if err != nil {
		if config.IsNotConfigured(err) {
			s.app.logger.Debug().Msg("Skipping cache pre-initialization: not configured")
			return nil
		}
		return err
	}
	release() // startup probe only verifies connectivity; release the lease immediately
	s.app.logger.Debug().Msg("Pre-initialized cache connection")
	return nil
}
```

Add `"github.com/gaborage/go-bricks/config"` to `app/slot.go`'s imports.

- [ ] **Step 4: Make `performPreInitialization` iterate**

In `app/app_builder.go`, replace `performPreInitialization` and delete the four helpers below it (`preInitDatabase`, `preInitMessaging`, `startupContext`, `preInitFatalComponent`, `preInitCache`) — `startupContext` moved to `app/slot.go` in Task 1, and leaving a second copy will not compile.

```go
// performPreInitialization attempts to establish connections during app startup.
// This reduces cold-start latency for single-tenant applications.
//
// Every kind is pre-initialized by its own slot, in registration order, under its OWN
// context budget sourced from app.startup.{database,messaging,cache} — the documented
// three-level fallback (component value > app.startup.timeout > built-in default) is
// resolved earlier, in config.applyStartupDefaults. Whether a failure is fatal is the
// slot's own verdict: database and messaging abort startup (a misconfigured backing store
// should fail fast), while the cache stays best-effort, because an unreachable cache is a
// runtime condition — cache *misconfiguration* already aborted earlier, at manager
// construction (CreateCacheManager).
func (b *Builder) performPreInitialization() {
	if b.err != nil {
		return
	}

	// Single parent context for the whole pre-init phase; each slot derives its own budget
	// from it via startupContext so all of them share one cancellation lineage. The context
	// is threaded as a parameter (never stored on the builder), matching the framework's
	// startup-at-Background precedent.
	parent := context.Background()
	b.logger.Debug().Msg("Performing pre-initialization for static single-tenant mode")

	for _, slot := range b.app.slots {
		err := slot.preInit(parent)
		if err == nil {
			continue
		}
		if slot.preInitFatal() {
			b.err = fmt.Errorf("%s connection failed during startup: %w", slot.name(), err)
			return
		}
		b.logger.Warn().Err(err).Msgf("Failed to pre-initialize %s connection (non-fatal)", slot.name())
	}
}
```

Then drop the now-unused imports from `app/app_builder.go`: `"time"` (only `startupContext` and the deleted helper signatures used it) and, if nothing else in the file references it, `"github.com/gaborage/go-bricks/config"` — check with `git grep -n "config\." app/app_builder.go` before removing; `WithConfig` calls `config.Validate` and `ConfigureRuntimeHelpers` calls `config.UntypedDatabaseSections` and `config.SourceTypeDynamic`, so `config` stays. `time` goes.

- [ ] **Step 5: Delete the moved test**

In `app/app_builder_test.go`, delete `TestPreInitCacheSkipsAbsentCache` outright (it drove `Builder.preInitCache`, which no longer exists; its coverage moved to `TestCacheSlotPreInitSkipsAbsentCache`). Then drop any import the deletion orphans — check with `go build ./app/` and `go vet ./app/`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./app/ -run 'TestSlotPreInitFatality|TestDatabaseSlotPreInit|TestCacheSlotPreInit|TestStreamsSlotPreInitIsANoop|TestPerformPreInitialization' -count=1 -race`
Expected: PASS

- [ ] **Step 7: Run the whole package**

Run: `go test ./app/ -count=1 -race`
Expected: PASS. `TestPerformPreInitializationUsesPerComponentTimeouts` and `TestPerformPreInitializationZeroBudgetUsesParentContext` are the load-bearing regressions here — both drive the real Builder chain and must stay green without edits, proving each slot still derives its own budget off one parent.

- [ ] **Step 8: Format and commit**

```bash
gofmt -w app/slot.go app/slot_test.go app/app_builder.go app/app_builder_test.go
```

```bash
cat > /tmp/slot-commit-2.txt <<'EOF'
refactor(app): move pre-initialization into the slots

performPreInitialization named the three kinds, hand-rolled a fatal-vs-
best-effort split across two helper shapes, and reached into the dependency
bundle for each manager. It now walks App.slots: each slot pre-initializes
its own kind under its own app.startup.<kind> budget derived from one shared
parent, and preInitFatal decides what a failure costs.

Every log line and error string is preserved: the phase Debug line, the
per-kind "Skipping %s pre-initialization: not configured" and
"Pre-initialized %s connection" Debug lines, the cache's
"Failed to pre-initialize cache connection (non-fatal)" WARN, and the fatal
"%s connection failed during startup: %w". A fatal failure still stops the
remaining kinds.

preInitDatabase, preInitMessaging, preInitCache and preInitFatalComponent
delete; startupContext moves to slot.go, where its only callers now live.
TestPreInitCacheSkipsAbsentCache moves to slot_test.go and drives the cache
slot directly rather than a Builder with no app.
EOF
git add app/slot.go app/slot_test.go app/app_builder.go app/app_builder_test.go
git commit -F /tmp/slot-commit-2.txt
```

---

## Task 3: the `start` and `stop` phases

**Files:**

- Modify: `app/slot.go` (replace the three placeholder `start` bodies), `app/lifecycle.go` (`prepareRuntime`, `Shutdown`), `app/prewarm.go` (delete three functions, retarget two doc comments)
- Modify (tests): `app/slot_test.go` (add), `app/lifecycle_test.go`, `app/prewarm_test.go`, `app/streams_setup_test.go`

**Interfaces:**

- Consumes from Tasks 1–2: `resourceSlot.start`, `resourceSlot.stop`, `App.slots`, `App.collectProbes`, `App.registerSlotCloser`, `recordingSlot`.
- Produces: `func (a *App) startSlots(ctx context.Context) error` and `func (a *App) stopSlots(ctx context.Context)` (both in `app/lifecycle.go`).

**Behaviour that must survive byte-for-byte:** the #907 fail-vs-warn consumer grading and its three messages; the AMQP consumer bootstrap running under a bare `context.Background()` while the pre-warms run under `prepareRuntime`'s own ctx; the multi-tenant skips; the per-kind pre-warm INFO/WARN/DEBUG lines; the **single** aggregate WARN `"Pre-warming completed with warnings"` carrying `fmt.Errorf("pre-warming issues (non-fatal): %w", errors.Join(...))`; a fatal streams start aborting startup; and ADR-029's `stop` → module `Shutdown` → closers order.

**The one intentional change:** `start` runs in the single registration order, so the database pre-warm and the messaging pre-warm now precede the streams start. See "Decisions taken up front", decision 2 — adopted, no atom line.

- [ ] **Step 1: Write the failing tests**

Append to `app/slot_test.go`:

```go
// TestStartSlotsRunsEveryKindInRegistrationOrder pins the walk itself: one order for every
// phase (spec decision 8), with no kind skipped.
func TestStartSlotsRunsEveryKindInRegistrationOrder(t *testing.T) {
	order := []string{}
	a := &App{logger: logger.New("error", false)}
	a.slots = []resourceSlot{
		&recordingSlot{kind: componentDatabase, order: &order},
		&recordingSlot{kind: componentMessaging, order: &order},
		&recordingSlot{kind: componentCache, order: &order},
		&recordingSlot{kind: componentStreams, order: &order},
	}

	require.NoError(t, a.startSlots(context.Background()))

	assert.Equal(t,
		[]string{"start:database", "start:messaging", "start:cache", "start:streams"},
		order)
}

// TestStartSlotsStopsAtTheFirstFatalKind pins that a kind that cannot start aborts startup
// there: a service that declared streams and cannot start them must not go on to serve HTTP.
func TestStartSlotsStopsAtTheFirstFatalKind(t *testing.T) {
	order := []string{}
	a := &App{logger: logger.New("error", false)}
	a.slots = []resourceSlot{
		&recordingSlot{kind: componentMessaging, order: &order, startFatal: assert.AnError},
		&recordingSlot{kind: componentStreams, order: &order},
	}

	err := a.startSlots(context.Background())

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"start:messaging"}, order)
}

// TestStartSlotsAggregatesAdvisoriesIntoOneWarn pins the pre-warm contract: advisory
// failures never fail startup and never multiply the operator's WARN count — both kinds'
// causes arrive under the one line prepareRuntime has always emitted.
func TestStartSlotsAggregatesAdvisoriesIntoOneWarn(t *testing.T) {
	order := []string{}
	rec := &recLogger{}
	a := &App{logger: rec}
	a.slots = []resourceSlot{
		&recordingSlot{kind: componentDatabase, order: &order, startAdvice: errors.New("db-advisory")},
		&recordingSlot{kind: componentMessaging, order: &order, startAdvice: errors.New("msg-advisory")},
	}

	require.NoError(t, a.startSlots(context.Background()),
		"pre-warming trouble is advisory: startup completes either way")

	event, emitted := loggedEvent(rec, "Pre-warming completed with warnings")
	require.True(t, emitted)
	assert.Equal(t, "warn", event.level)
	assert.Contains(t, event.err, "pre-warming issues (non-fatal)")
	assert.Contains(t, event.err, "db-advisory")
	assert.Contains(t, event.err, "msg-advisory")
}

// TestStartSlotsStaysSilentWithoutAdvisories is the negative half.
func TestStartSlotsStaysSilentWithoutAdvisories(t *testing.T) {
	order := []string{}
	rec := &recLogger{}
	a := &App{logger: rec}
	a.slots = []resourceSlot{&recordingSlot{kind: componentDatabase, order: &order}}

	require.NoError(t, a.startSlots(context.Background()))

	_, emitted := loggedEvent(rec, "Pre-warming completed with warnings")
	assert.False(t, emitted, "a clean start must emit no pre-warm WARN")
}

// TestStopSlotsRunsEveryKindInRegistrationOrder pins the shutdown walk. ADR-029 places it
// before module Shutdown, which TestShutdownStopsServerBeforeModules covers end to end.
func TestStopSlotsRunsEveryKindInRegistrationOrder(t *testing.T) {
	order := []string{}
	a := &App{logger: logger.New("error", false)}
	a.slots = []resourceSlot{
		&recordingSlot{kind: componentMessaging, order: &order},
		&recordingSlot{kind: componentStreams, order: &order},
	}

	a.stopSlots(context.Background())

	assert.Equal(t, []string{"stop:messaging", "stop:streams"}, order)
}

// TestDatabaseSlotStartSkipsMultiTenant pins the deployment-mode check inside the slot:
// multi-tenant resources resolve per tenant, so the fixed "" key is never warmed. The
// provider always refuses, so warming it would surface as an advisory error.
func TestDatabaseSlotStartSkipsMultiTenant(t *testing.T) {
	log := logger.New("error", false)
	cfg := defaultTestConfig()
	cfg.Multitenant.Enabled = true
	dbManager := database.NewDbManager(staticDBConfigProvider{err: errNeverReachTheConnector}, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return dbtesting.NewTestDB(dbTypePostgres), nil
		})
	t.Cleanup(func() { assert.NoError(t, dbManager.Close()) })

	a := &App{cfg: cfg, logger: log, dbManager: dbManager}
	a.installSlots(slotInputs{})

	advisory, fatal := a.slots[0].start(context.Background())

	assert.NoError(t, fatal)
	assert.NoError(t, advisory, "multi-tenant startup must not pre-warm the fixed \"\" key")
}

// TestDatabaseSlotStartReportsPreWarmFailureAsAdvisory pins the other arm: a refused
// pre-warm is reported, never fatal.
func TestDatabaseSlotStartReportsPreWarmFailureAsAdvisory(t *testing.T) {
	log := logger.New("error", false)
	dbManager := database.NewDbManager(staticDBConfigProvider{err: errNeverReachTheConnector}, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return dbtesting.NewTestDB(dbTypePostgres), nil
		})
	t.Cleanup(func() { assert.NoError(t, dbManager.Close()) })

	a := &App{cfg: defaultTestConfig(), logger: log, dbManager: dbManager}
	a.installSlots(slotInputs{})

	advisory, fatal := a.slots[0].start(context.Background())

	assert.NoError(t, fatal, "pre-warming is never fatal")
	require.Error(t, advisory)
	assert.Contains(t, advisory.Error(), "database pre-warming failed")
	assert.ErrorIs(t, advisory, errNeverReachTheConnector)
}

// TestStreamsSlotStartRegistersItsCloser pins the half of the runtime registration the slot
// now owns: prepareStreamConsumers produces the manager, the slot puts it on the FIFO close
// list. A streams-free service registers nothing.
func TestStreamsSlotStartRegistersItsCloser(t *testing.T) {
	t.Run("no_declarations_registers_nothing", func(t *testing.T) {
		a := newStreamsApp(t, config.StreamsConfig{}, &minimalModule{name: "plain"})
		a.installSlots(slotInputs{})

		advisory, fatal := a.slots[3].start(context.Background())

		require.NoError(t, fatal)
		require.NoError(t, advisory)
		assert.Nil(t, a.streamsManager)
		assert.Empty(t, a.closers)
	})

	t.Run("failed_start_registers_nothing", func(t *testing.T) {
		a := newStreamsApp(t, config.StreamsConfig{URI: unreachableStreamURI},
			&streamModule{name: "orders", declaration: declareOneConsumer})
		a.installSlots(slotInputs{})

		_, fatal := a.slots[3].start(context.Background())

		require.Error(t, fatal, "a service that declared streams and cannot start them must abort")
		assert.Nil(t, a.streamsManager)
		assert.Empty(t, a.closers)
	})
}
```

Add `"errors"` to `app/slot_test.go`'s imports if Step 1 of Task 1 did not already (it did, for `errNeverReachTheConnector`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./app/ -run 'TestStartSlots|TestStopSlots|TestDatabaseSlotStart|TestStreamsSlotStartRegistersItsCloser' -count=1`
Expected: FAIL — compile error `a.startSlots undefined` and `a.stopSlots undefined`.

- [ ] **Step 3: Write the real `start` bodies**

In `app/slot.go`, delete the three `start` placeholders and write the real ones beside each slot's other methods.

`databaseSlot`, after `preInit`:

```go
// start pre-warms the single-tenant connection so the first request does not pay the dial.
// Advisory only: a cold database is a runtime condition, and pre-init has already made a
// *misconfigured* one fatal.
func (s *databaseSlot) start(ctx context.Context) (advisory, fatal error) {
	if s.app.multiTenant() {
		return nil, nil
	}
	if s.app.dbManager == nil {
		s.app.logger.Debug().Msg("Skipping single-tenant database pre-warming: manager unavailable")
		return nil, nil
	}

	if err := s.app.preWarmDatabase(ctx); err != nil {
		if config.IsNotConfigured(err) {
			s.app.logger.Debug().Msg("Skipping single-tenant database pre-warming: not configured")
			return nil, nil
		}
		s.app.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant database connection")
		return fmt.Errorf("database pre-warming failed: %w", err), nil
	}

	s.app.logger.Info().Msg("Pre-warmed single-tenant database connection")
	return nil, nil
}
```

`messagingSlot`, after `preInit`:

```go
// start runs the kind's two runtime steps in the order prepareRuntime always ran them: the
// consumer bootstrap, whose failure is fatal once consumers were declared (#907), then the
// single-tenant pre-warm, which is advisory.
func (s *messagingSlot) start(ctx context.Context) (advisory, fatal error) {
	decls := s.app.messagingDeclarations

	// Bare context.Background(), not ctx: consumers outlive prepareRuntime and are stopped by
	// stop() (ADR-029), never by the startup context — EnsureConsumers severs the caller's
	// cancellation internally, so only its values would have carried.
	if err := s.app.prepareRuntimeConsumers(context.Background(), decls); err != nil {
		return nil, err
	}

	if s.app.multiTenant() {
		return nil, nil
	}
	if s.app.messagingManager == nil {
		s.app.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: manager unavailable")
		return nil, nil
	}

	if err := s.app.preWarmMessaging(ctx, decls); err != nil {
		if config.IsNotConfigured(err) {
			s.app.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: not configured")
			return nil, nil
		}
		s.app.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant messaging")
		return fmt.Errorf("messaging pre-warming failed: %w", err), nil
	}

	s.app.logger.Info().Msg("Pre-warmed single-tenant messaging")
	return nil, nil
}
```

`streamsSlot`, after `preInitFatal`:

```go
// start builds the stream environment and starts the declared consumers and publishers,
// then puts the manager on the FIFO close list. A service that declared streams and cannot
// start them would serve HTTP while consuming nothing and publishing nowhere, so the failure
// is fatal. PR5 folds prepareStreamConsumers' body in here.
func (s *streamsSlot) start(ctx context.Context) (advisory, fatal error) {
	if err := s.app.prepareStreamConsumers(ctx); err != nil {
		return nil, err
	}
	s.app.registerSlotCloser(s)
	return nil, nil
}
```

Add `"fmt"` to `app/slot.go`'s imports.

- [ ] **Step 4: Rewrite `prepareRuntime` and `Shutdown`'s stop phase**

In `app/lifecycle.go`, replace the head of `prepareRuntime` — everything from `decls := a.messagingDeclarations` through the pre-warm block — so the whole function reads:

```go
// prepareRuntime prepares the application for runtime execution. ctx is the
// startup context: components it starts that outlive startup inherit that
// context's values rather than beginning from a bare context.Background().
// The AMQP consumer step is the one exception — see messagingSlot.start.
func (a *App) prepareRuntime(ctx context.Context) error {
	if err := a.buildMessagingDeclarations(); err != nil {
		return err
	}

	if err := a.assertMessagingConfiguredIfDeclared(a.messagingDeclarations); err != nil {
		return err
	}

	if err := a.startSlots(ctx); err != nil {
		return err
	}

	// The streams slot only builds its manager in start, so the probe set is collected again
	// here rather than at build time. Safe because prepareRuntime is single-threaded and
	// completes before the server starts serving /ready.
	a.healthProbes = a.collectProbes()

	// Register debug endpoints if enabled
	if err := a.registerDebugHandlers(); err != nil {
		return err
	}

	// Register scheduled jobs (after all modules initialized, before routes)
	if err := a.registry.RegisterJobs(); err != nil {
		return err
	}

	if err := a.applyGlobalMiddleware(); err != nil {
		return err
	}

	a.registry.RegisterRoutes(a.server.ModuleGroup())
	if err := a.checkRouteConflicts(); err != nil {
		return err
	}
	a.startMaintenanceLoops()

	return nil
}

// startSlots runs every kind's start phase in registration order. A fatal error aborts
// startup at the kind that reported it, so nothing after it runs. Advisory errors — the
// best-effort single-tenant pre-warms — are aggregated into the one WARN prepareRuntime has
// always emitted, and never fail startup.
func (a *App) startSlots(ctx context.Context) error {
	var advisories []error
	for _, slot := range a.slots {
		advisory, fatal := slot.start(ctx)
		if fatal != nil {
			return fatal
		}
		if advisory != nil {
			advisories = append(advisories, advisory)
		}
	}

	if len(advisories) > 0 {
		a.logger.Warn().
			Err(fmt.Errorf("pre-warming issues (non-fatal): %w", errors.Join(advisories...))).
			Msg("Pre-warming completed with warnings")
	}
	return nil
}

// stopSlots halts every kind's inbound work in registration order, before modules are torn
// down (ADR-029). Connections stay open — the close phase, after module Shutdown, owns those.
func (a *App) stopSlots(ctx context.Context) {
	for _, slot := range a.slots {
		slot.stop(ctx)
	}
}
```

In `app/lifecycle.go`, `Shutdown`, replace step 2's two calls:

```go
	// 2. Stop AMQP consumers from accepting new messages (connections are closed later via
	//    the messaging-manager closer). Done before module shutdown so the framework stops
	//    delivering fresh messages to modules that are about to be torn down.
	a.shutdownConsumers()
	a.shutdownStreamConsumers()
```

with

```go
	// 2. Stop each kind's inbound work (connections are closed later, in step 6, via the
	//    slots' closers). Done before module shutdown so the framework stops delivering fresh
	//    messages to modules that are about to be torn down.
	a.stopSlots(ctx)
```

- [ ] **Step 5: Delete the three functions the slots replaced**

In `app/prewarm.go`, delete `preWarmSingleTenant`, `attemptDatabasePreWarm` and `attemptMessagingPreWarm` (everything from the `// preWarmSingleTenant pre-warms connections` comment through the closing brace of `attemptMessagingPreWarm`). Keep `preWarmDatabase`, `preWarmMessaging`, `preWarmReadyOutcome`, `publisherReadinessTimeout` and `awaitPublisherReady`.

Retarget the two doc comments that named the deleted callers:

```go
// preWarmDatabase leases the fixed "" key to verify connectivity and releases it
// immediately. attemptDatabasePreWarm holds the manager nil check.
```

becomes

```go
// preWarmDatabase leases the fixed "" key to verify connectivity and releases it
// immediately. databaseSlot.start holds the manager nil check and the deployment-mode gate.
```

and

```go
// preWarmMessaging ensures consumers for the fixed "" key and waits, bounded, for the
// publisher to report ready. attemptMessagingPreWarm holds the manager nil check.
```

becomes

```go
// preWarmMessaging ensures consumers for the fixed "" key and waits, bounded, for the
// publisher to report ready. messagingSlot.start holds the manager nil check and the
// deployment-mode gate.
```

Then drop the imports the deletion orphans from `app/prewarm.go`: `"errors"` (only `preWarmSingleTenant` used `errors.Join`) — verify with `git grep -n "errors\." app/prewarm.go`; `config` stays only if something still calls `config.IsNotConfigured` there, which after this deletion it does not, so drop `"github.com/gaborage/go-bricks/config"` too. Confirm with `go build ./app/`.

- [ ] **Step 6: Retarget the tests that drove the deleted functions**

`app/lifecycle_test.go` — `newLifecycleCheckAppWithLogger` must install the slots, because `prepareRuntime` now reaches its per-kind work only through them:

```go
func newLifecycleCheckAppWithLogger(t *testing.T, cfg *config.Config, log logger.Logger) *App {
	t.Helper()
	deps := &ModuleDeps{Logger: log, Config: cfg}
	a := &App{
		cfg:      cfg,
		logger:   log,
		registry: NewModuleRegistry(deps),
		server:   newMockServer(),
		closers:  []namedCloser{},
	}
	a.installSlots(slotInputs{})
	return a
}
```

The slots hold the App and read its manager fields live, so the tests that assign `a.dbManager = dbManager` *after* this constructor returns keep working unchanged. `TestPrepareRuntimePropagatesContextToPreWarm`, `TestPrepareRuntimeWarnsOnlyWhenPreWarmFails` and `TestPrepareRuntimeSkipsPreWarmInMultiTenantMode` need no other edit — they are the regression pins for the aggregate WARN and the multi-tenant skip surviving the move.

`app/lifecycle_test.go` — `TestShutdownTiming`'s literal `&App{…}` (around line 132) builds an App with no slots, so `stopSlots` is a no-op there; that is correct (it has no managers) and needs no edit.

`app/prewarm_test.go` — replace the three `preWarmSingleTenant` drivers with `messagingSlot.start` drivers. `newPreWarmApp` gains one line so the slots exist:

```go
func newPreWarmApp(log logger.Logger, manager *messaging.Manager, readyTimeout time.Duration) *App {
	a := &App{
		logger:           log,
		messagingManager: manager,
		cfg: &config.Config{
			Messaging: config.MessagingConfig{
				Reconnect: config.ReconnectConfig{ReadyTimeout: readyTimeout},
			},
		},
	}
	a.installSlots(slotInputs{})
	return a
}
```

Then replace `TestPreWarmSingleTenantSkipsAbsentManagers` with:

```go
// TestSlotStartSkipsAbsentManagers pins the absence guard: with neither manager built, the
// start phase is a silent no-op and never reports a problem.
func TestSlotStartSkipsAbsentManagers(t *testing.T) {
	a := &App{logger: logger.New("debug", true), cfg: &config.Config{}}
	a.installSlots(slotInputs{})

	for _, slot := range a.slots {
		advisory, fatal := slot.start(context.Background())
		require.NoError(t, fatal, slot.name())
		require.NoError(t, advisory, slot.name())
	}
}
```

and in `TestPreWarmSingleTenantAwaitsPublisherReadiness`, `TestPreWarmSingleTenantContinuesWhenPublisherNeverReady` and `TestPreWarmSingleTenantPropagatesContextCancellation`, replace each

```go
	err := a.preWarmSingleTenant(ctx, nil)
```

(three sites; the first two pass `context.Background()`) with

```go
	err, fatal := a.slots[1].start(ctx)
	require.NoError(t, fatal, "pre-warming is never fatal")
```

Rename the three functions to `TestMessagingSlotStartAwaitsPublisherReadiness`, `TestMessagingSlotStartContinuesWhenPublisherNeverReady` and `TestMessagingSlotStartPropagatesContextCancellation`. Two assertions change shape because `start` wraps the cause:

- in `…ContinuesWhenPublisherNeverReady`, `assert.NoError(t, err)` stays (a not-ready-in-time publisher is not an error at all — `preWarmMessaging` returns nil after its WARN);
- in `…PropagatesContextCancellation`, `assert.ErrorIs(t, err, context.DeadlineExceeded)` stays — `errors.Is` follows the `%w` chain through `"messaging pre-warming failed: %w"`.

`app/streams_setup_test.go` — the two remaining closer assertions belong to the slot now. In `TestPrepareStreamConsumersWithoutDeclarationsIsNoop` and `TestPrepareStreamConsumersRejectsMultiTenantBypass`, keep `assert.Empty(t, a.closers)` (both still true: `prepareStreamConsumers` no longer registers anything, and neither does the slot when it is not called). No further edit.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./app/ -run 'TestStartSlots|TestStopSlots|TestDatabaseSlotStart|TestStreamsSlotStartRegistersItsCloser|TestMessagingSlotStart|TestSlotStartSkipsAbsentManagers|TestPrepareRuntime' -count=1 -race`
Expected: PASS

- [ ] **Step 8: Run the whole package**

Run: `go test ./app/ -count=1 -race`
Expected: PASS. The load-bearing regressions here are `TestPrepareRuntimeWarnsOnlyWhenPreWarmFails` (one aggregate WARN carrying the cause, and silence on a clean run), `TestPrepareRuntimePropagatesContextToPreWarm` (the startup ctx still reaches the database pre-warm), `TestPrepareRuntimeSkipsPreWarmInMultiTenantMode`, `TestPrepareRuntimeConsumers*` (the #907 grading), `TestShutdownStopsServerBeforeModules` (ADR-029), and every `TestPrepareStreamConsumers*`.

- [ ] **Step 9: Prove the whole tree still builds and vets**

Run: `go build ./... && go vet ./...`
Expected: no output. `go vet` matters because `resourceSlot` is satisfied by four types plus a test stub, and `go build` alone skips test files.

- [ ] **Step 10: Format and commit**

```bash
gofmt -w app/slot.go app/slot_test.go app/lifecycle.go app/prewarm.go \
  app/lifecycle_test.go app/prewarm_test.go app/streams_setup_test.go
```

```bash
cat > /tmp/slot-commit-3.txt <<'EOF'
refactor(app): run the start and stop phases through the slots

prepareRuntime named the AMQP consumer bootstrap, the streams start and the
two pre-warms as four separate steps, and Shutdown named two stop calls. Both
now walk App.slots. Each slot's start returns an advisory error and a fatal
one: the consumer bootstrap and the streams start are fatal, the two
single-tenant pre-warms are advisory and are joined into the single
"Pre-warming completed with warnings" WARN prepareRuntime has always emitted.
stopSlots keeps ADR-029's order — stop inbound work, then module Shutdown,
then closers.

One order change: the start phase now uses the single registration order
database -> messaging -> cache -> streams, so the two pre-warms precede the
streams start where they used to follow it. Nothing depends on the old
order (streams shares no state with the AMQP or database managers, and
neither pre-warm feeds either fatal step), no /ready key, status code, config
key or exported symbol moves, and the only cost is that a startup destined to
fail on streams first pays a bounded pre-warm.

The streams slot registers its own closer once prepareStreamConsumers has
produced the manager, and prepareRuntime re-collects the probe set after the
start phase, so the streams probe still appears exactly where the runtime
append put it. preWarmSingleTenant, attemptDatabasePreWarm and
attemptMessagingPreWarm delete; their bodies are the two slots' start.
EOF
git add app/slot.go app/slot_test.go app/lifecycle.go app/prewarm.go \
  app/lifecycle_test.go app/prewarm_test.go app/streams_setup_test.go
git commit -F /tmp/slot-commit-3.txt
```

---

## Task 4: verification sweep and gates

No ADR and no migration atom land in this PR. ADR-067 already describes the slots and lands with PR2 (its Task 4); nothing this PR changes is visible in the `/ready` body, the debug body, a status code, a config key or an exported symbol, so `wiki/migrations.md` needs no `[C60.5]`. `CLAUDE.md`'s `## Breaking Changes` is untouched — this PR is not breaking.

**Files:** none modified. This task is verification only.

- [ ] **Step 1: Prove no exported symbol moved**

```bash
git diff origin/main...HEAD -- 'app/*.go' ':!app/*_test.go' | grep -E '^[-+](func |type |var |const )[A-Z]' || echo "no exported declarations changed"
```

Expected: `no exported declarations changed`. If anything prints, stop and report it to the controller — the PR would need an ADR and an atom, and this plan's Global Constraints forbid it.

- [ ] **Step 2: Prove the kind names are no longer hand-enumerated**

```bash
git grep -n "dbManager != nil" -- app/ ':!app/*_test.go'
git grep -n "cacheManager != nil" -- app/ ':!app/*_test.go'
```

Expected: every remaining hit is inside `app/slot.go` (the slots' own `closer`/`preInit`/`start` guards), `app/resource_provider.go`, or `app/bootstrap.go` / `app/managers.go` (construction, which card 2 owns and this PR does not touch). No hit in `app/app_builder.go` or `app/lifecycle.go`.

- [ ] **Step 3: Prove the deleted helpers are gone**

```bash
git grep -nE "rebuildClosersAndHealth|probeInputs|createHealthProbes|preInitFatalComponent|preWarmSingleTenant|attemptDatabasePreWarm|attemptMessagingPreWarm" -- app/ \
  && echo "STILL PRESENT — delete before handing over" || echo "all removed"
```

Expected: `all removed`.

- [ ] **Step 4: Run the machine gates**

Run `make check` in the background (per CLAUDE.md; it mirrors CI and needs Node for npx):

```bash
make check
```

Expected: PASS. The linters most likely to fire on this diff are `unused` (an orphaned helper missed in Task 2 or 3), `gci`/`gofumpt` (import ordering in the new `app/slot.go` — `standard`, then third party, then `prefix(github.com/gaborage/go-bricks)`), and `dupl` at threshold 100 (the four slots' `closer` methods are three lines each, well under it, but check the finding rather than assuming). Fix findings; never `//nolint`.

Then, once `make check` is green and nothing further changed:

```bash
make mutate
```

Expected: a summary naming a non-zero mutant count on changed lines and zero survivors. An empty result is **not** a pass — commit first (the scope is `merge-base..HEAD`), and confirm the run printed `(N mutants on changed lines)` with N > 0.

- [ ] **Step 5: Run the pre-push agent gates in order**

`/simplify` → `make check` if it changed code → `/security-audit` → `make check` if it changed code → `/code-review` (CodeRabbit) → `make check` + `make mutate` if it changed code, then `/code-review` again.

The security-relevant surface a reviewer should be pointed at: `probeDescription.name` still reaches the unauthenticated `/ready` body, and every slot's `probe()` feeds it only the four fixed component constants; the public-stats allowlists are untouched; and the `stop` → module `Shutdown` → close ordering (ADR-029) now runs through two loops rather than five explicit calls.

- [ ] **Step 6: Report to the controller**

State: the three commits, the `make mutate` mutant count, whether any gate changed code (and therefore which gates re-ran), and confirmation that Steps 1–3 printed their expected output. Do not push; the controller owns the push and the stacked-PR bookkeeping.

---

## Self-review

**1. Spec coverage.** Lifecycle-slots decisions 1–5 and 7:

- *Decision 1 (unexported `resourceSlot`, four structs, compiler-checked completeness, App keeps typed manager fields):* Task 1, Step 3 — the interface, four structs, the four `var _ resourceSlot = …` assertions; the `App` struct keeps `dbManager`/`messagingManager`/`cacheManager`/`streamsManager` untouched (Task 1, Step 4 only *adds* `slots`).
- *Decision 2 (phases: probe, preInit+fatal, start, stop, close; no maintenance phase):* the interface has exactly those six methods and no maintenance method; Task 1 covers probe + close, Task 2 preInit + preInitFatal, Task 3 start + stop. `startMaintenanceLoops` is untouched and named in the Global Constraints as PR4's.
- *Decision 3 (maintenance is manager-side):* explicitly **out of scope** — PR4.
- *Decision 4 (Builder steps keep their names and iterate slots):* Task 1, Step 5 — `CreateHealthProbes` and `RegisterClosers` keep their names and become one-line loops.
- *Decision 5 (streams slot exists at build time, constructs+starts in start, ADR-029 preserved by the stop/close split):* Task 1 installs `streamsSlot` at build time; Task 3 gives it `start` and `stop`. It exists at build time **with its probe withheld** rather than reporting `disabled` — deviation stated and argued under "Decisions taken up front", decision 1, on the grounds that the spec's own readiness decision 5 says streams keeps registering at runtime until PR5.
- *Decision 7 (slices):* PR3's boundary is set — streams_setup.go's body stays for PR5, per "Decisions taken up front", decision 4.

Controller-brief items 1–5 all have a task: (1) Task 1, (2) Task 2, (3) Task 3, (4) decided in "Decisions taken up front" #4 (deferred to PR5), (5) Task 4.

**2. Placeholder scan.** No "TBD", "implement later", "similar to Task N", or "add appropriate error handling". Every code step carries the literal code. One deliberate false-start appears in Task 1, Step 3 (the `builderPreInit*` delegates, immediately retracted with the reason) — an executor reading straight through applies the second block; the retraction is explicit ("**Stop.** … Delete the block above and use this instead"). Two greps that can legitimately print nothing (`git grep … && echo "STILL PRESENT"`) are written with an `|| echo` fallback so a zero-hit grep's exit code 1 does not read as a failed step.

**3. Type consistency.**

- `probe() (probeDescription, bool)` — declared once in Task 1 Step 3, implemented by all four slots there and by `recordingSlot` in Task 2 Step 1; consumed only by `collectProbes`.
- `start(ctx context.Context) (advisory, fatal error)` — same signature in the interface (Task 1), the placeholders (Task 1), the real bodies (Task 3 Step 3), `cacheSlot` (Task 1, already final), and `recordingSlot` (Task 2).
- `closer() (namedCloser, bool)` — `namedCloser` is the existing `app/internal_types.go` struct with fields `name string` and `closer interface{ Close() error }`; every `closer()` body constructs it with those two field names.
- `slotInputs{cacheAbsent bool}` — one field, used identically at `Builder.CreateApp` (Task 1 Step 5), the fixture (Task 1 Step 7), `cacheSlot.absent` (Task 1 Step 3) and `TestCacheSlotTakesAbsenceFromItsInputs` / `TestCacheSlotPreInitSkipsAbsentCache`.
- `installSlots` / `collectProbes` / `registerSlotCloser` / `registerSlotClosers` / `multiTenant` / `startSlots` / `stopSlots` — each declared exactly once (`installSlots`, `collectProbes`, `registerSlotCloser`, `registerSlotClosers` in `app/slot.go`; `multiTenant` in `app/app.go`; `startSlots`, `stopSlots` in `app/lifecycle.go`) and spelled identically at every call site.
- `startupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc)` — moved verbatim to `app/slot.go` in Task 1 Step 3 and deleted from `app/app_builder.go` in Task 2 Step 4; the plan flags that leaving both copies will not compile.
- Existing names reused without redefinition: `probeDescription`, `disabledProbe`, `databaseProbe`, `messagingProbe`, `cacheProbe`, `streamsProbe`, `Prober`, `rootCacheAbsent`, `prepareRuntimeConsumers`, `preWarmDatabase`, `preWarmMessaging`, `shutdownConsumers`, `shutdownStreamConsumers`, `prepareStreamConsumers`, `registerCloser`, `componentDatabase`/`componentMessaging`/`componentCache`/`componentStreams`, `recLogger`, `loggedEvent`, `staticDBConfigProvider`, `defaultTestConfig`, `dbTypePostgres`, `unreachableStreamURI`, `newStreamsApp`, `streamModule`, `minimalModule`, `declareOneConsumer`, `createTestCacheManager`, `createTestCacheManagerWithGetError`, `createTestCacheManagerWithConnector`, `statsActiveConnectionsKey`.

**Size note for the controller:** the estimated diff is ~230 new production lines in `app/slot.go` against ~200 deleted across `app_builder.go`/`prewarm.go`/`app.go`, plus ~330 new test lines against ~120 deleted — roughly 550 changed LoC, above the ~400 threshold. The task boundaries are clean split points if the stack should be finer: Tasks 1–2 (probe · pre-init · close) form one self-contained PR that builds and passes on its own, and Task 3 (start · stop) forms the next.
