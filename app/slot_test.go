package app

import (
	"context"
	"errors"
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
