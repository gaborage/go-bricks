package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
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

// errNeverReachTheConnector fails any lease the absent arm should never attempt.
var errNeverReachTheConnector = errors.New("the absent arm must never reach the connector")

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

// assertCloserIdentity pins that every registered closer is the very manager its own slot
// holds, so a slot handing over the wrong one fails here instead of at shutdown, where the
// names alone would still read correctly.
func assertCloserIdentity(t *testing.T, a *App) {
	t.Helper()
	want := map[string]any{
		databaseCloserName:  a.dbManager,
		messagingCloserName: a.messagingManager,
		cacheCloserName:     a.cacheManager,
		streamsCloserName:   a.streamsManager,
	}
	for _, c := range a.closers {
		assert.Same(t, want[c.name], c.closer, "closer %q must be its slot's own manager", c.name)
	}
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
			assertCloserIdentity(t, a)
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
	assertCloserIdentity(t, a)
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

// TestStreamsSlotContributesItsCloserOnceItsManagerExists is the close-walk half of the
// same withholding rule: the kind stays out of the FIFO list until its manager exists,
// which is what keeps a streams-free service's close list to the classic kinds.
func TestStreamsSlotContributesItsCloserOnceItsManagerExists(t *testing.T) {
	a := newSlotTestApp(t, false, false)
	a.registerSlotClosers()
	require.Empty(t, closerNames(a), "a streams-free service closes nothing")

	a.streamsManager = streams.NewManager(streams.ManagerOptions{
		URI:    unreachableStreamURI,
		Logger: a.logger,
	})
	t.Cleanup(func() { _ = a.streamsManager.Close() })
	a.closers = nil

	a.registerSlotClosers()
	assert.Equal(t, []string{streamsCloserName}, closerNames(a))
	assertCloserIdentity(t, a)
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

// TestSlotPreInitFatality pins the classification the spec fixes: database and messaging
// abort startup, cache is best-effort, streams has no pre-init at all. The table binds each
// verdict to its kind's name, so a reordered slot list fails here rather than silently
// swapping two kinds' fatality.
func TestSlotPreInitFatality(t *testing.T) {
	a := newSlotTestApp(t, false, false)
	require.Len(t, a.slots, 4)

	cases := []struct {
		name  string
		kind  string
		why   string
		index int
		fatal bool
	}{
		{name: "database_is_fatal", index: 0, kind: componentDatabase, fatal: true, why: "a misconfigured database must fail startup"},
		{name: "messaging_is_fatal", index: 1, kind: componentMessaging, fatal: true, why: "a misconfigured broker must fail startup"},
		{name: "cache_is_best_effort", index: 2, kind: componentCache, fatal: false, why: "an unreachable cache is a runtime condition"},
		{name: "streams_is_best_effort", index: 3, kind: componentStreams, fatal: false, why: "streams has no pre-init"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slot := a.slots[tc.index]
			require.Equal(t, tc.kind, slot.name())
			assert.Equal(t, tc.fatal, slot.preInitFatal(), tc.why)
		})
	}
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
// zero. Moved here from app_builder_test.go, where it drove the Builder's own cache arm.
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

// recordingSlot is a resourceSlot stand-in that records which phase ran on which kind, so
// the walks can be pinned on order and short-circuiting without standing up four real
// managers. Every field defaults to "this phase succeeds and does nothing".
type recordingSlot struct {
	order        *[]string
	preInitErr   error
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

func (s *recordingSlot) closer() (namedCloser, bool) { return namedCloser{}, false }

var _ resourceSlot = (*recordingSlot)(nil)

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
