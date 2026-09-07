package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/racedetect"
	"github.com/gaborage/go-bricks/logger"
)

// sealedSlot is the second adapter at the slot seam: a slot that carries the production
// readiness storage and whose every other phase does nothing. A nil description is a kind
// that renders nothing — either one that never sealed, or one whose describe withheld a
// description (the streams kind before its manager exists).
type sealedSlot struct {
	sealedReadiness
}

func (s *sealedSlot) describe() (probeDescription, bool) {
	if s.description == nil {
		return probeDescription{}, false
	}
	return *s.description, true
}

func (s *sealedSlot) preInit(context.Context) error                 { return nil }
func (s *sealedSlot) preInitFatal() bool                            { return false }
func (s *sealedSlot) start(context.Context) (advisory, fatal error) { return nil, nil }
func (s *sealedSlot) stop(context.Context)                          {}
func (s *sealedSlot) closer() (namedCloser, bool)                   { return namedCloser{}, false }

var _ resourceSlot = (*sealedSlot)(nil)

// judgeOf is the judge over one pre-sealed slot per description — the seam the two
// readiness views are driven through. The judge is started: the fixtures stand in for an
// application whose startSlots walk already completed.
func judgeOf(descriptions ...probeDescription) readinessJudge {
	slots := make([]resourceSlot, 0, len(descriptions))
	for i := range descriptions {
		slots = append(slots, &sealedSlot{sealedReadiness{kind: descriptions[i].name, description: &descriptions[i]}})
	}
	return readinessJudge{slots: slots, started: true}
}

// installSealedSlots stands in for CreateApp's slot list plus the seal startSlots performs:
// one pre-sealed slot per description, and the judge over them.
func installSealedSlots(a *App, descriptions ...probeDescription) {
	j := judgeOf(descriptions...)
	installSlotList(a, j.slots...)
}

// installSlotList installs an explicit slot list and the judge over it, for the tests that
// need an absent kind (a slot sealing nothing) beside sealed ones.
func installSlotList(a *App, slots ...resourceSlot) {
	a.slots = slots
	a.judge = readinessJudge{slots: slots, started: true}
}

// sealAndJudge stands in for the startSlots seal plus Builder.CreateHealthProbes, for the
// fixtures that run neither: every installed slot seals what it describes, and the judge is
// installed over that same list.
func sealAndJudge(a *App) {
	for _, s := range a.slots {
		s.seal(s.describe())
	}
	a.judge = readinessJudge{slots: a.slots, started: true}
}

// TestReadyReportsEveryKindTheSlotsSealed pins the judgement path: /ready renders one entry
// per kind whose slot sealed a description, in slot order, and nothing for a kind that
// sealed none.
func TestReadyReportsEveryKindTheSlotsSealed(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"}}
	app := &App{cfg: cfg, logger: logger.New("error", false)}
	installSealedSlots(app,
		describe(componentDatabase, true, nil, nil, databasePublicStats),
		disabledProbe(componentCache),
	)
	// The streams kind before its manager exists: installed, sealing nothing.
	installSlotList(app, append(app.slots, &sealedSlot{sealedReadiness{kind: componentStreams}})...)

	body, code := runReadyCheck(t, app, cfg)

	require.Equal(t, 200, code)
	assert.Equal(t, healthyStatus, body[componentDatabase])
	assert.Equal(t, disabledStatus, body[componentCache])
	assert.NotContains(t, body, componentStreams)
	assert.NotContains(t, body, componentStreams+statsSuffix)
}

// TestJudgeBeforeTheStartWalkFailsClosed pins the started fact rather than the empty-report
// proxy it replaced: a judge installed over slots that have not started answers the gate
// 503 under the readiness component, while a started judge with nothing to report answers a
// normal 200. Unreachable through Run — CreateHealthProbes precedes prepareRuntime — but
// the gate must fail closed if it ever is reached.
func TestJudgeBeforeTheStartWalkFailsClosed(t *testing.T) {
	slots := []resourceSlot{&sealedSlot{sealedReadiness{kind: componentDatabase}}}

	_, blocking, found := readinessJudge{slots: slots}.gate(context.Background())

	require.True(t, found, "an application that has not started may not take traffic")
	assert.Equal(t, componentReadiness, blocking.Name)
	assert.Equal(t, unhealthyStatus, blocking.Status)
	assert.True(t, blocking.Critical)
	require.Error(t, blocking.Err)

	_, _, startedFound := readinessJudge{slots: slots, started: true}.gate(context.Background())
	assert.False(t, startedFound, "an empty report from a started application is a normal 200")
}

// judgementAllocs measures what one gate judgement costs BEYOND running the same
// descriptions directly: the walk's own allocations. The fixtures are stats-less
// lease-less descriptions, so the per-kind cost of Run is identical on both sides and
// cancels, leaving the traversal.
func judgementAllocs(kinds int) float64 {
	descriptions := make([]probeDescription, 0, kinds)
	for range kinds {
		descriptions = append(descriptions, probeDescription{
			name: componentDatabase,
			live: func(context.Context) error { return nil },
		})
	}
	judge := judgeOf(descriptions...)
	ctx := context.Background()

	direct := testing.AllocsPerRun(100, func() {
		for i := range descriptions {
			_ = descriptions[i].Run(ctx)
		}
	})
	viaJudge := testing.AllocsPerRun(100, func() {
		_, _, _ = judge.gate(ctx)
	})
	return viaJudge - direct
}

// TestJudgementAllocsStableAcrossKindCount is the tripwire guard on the judgement path
// (ADR-026's shape, ADR-066 as amended): readiness asks each slot for the description it
// sealed, so doubling the kinds must not double what the traversal costs. A description
// rebuilt per request — fresh closures, a Prober boxed per kind — would make the two
// counts diverge. The counts are compared to each other rather than to an absolute pin, so
// the guard says nothing about how many allocations one judgement costs.
//
// It measures the traversal only, by subtracting the cost of running the very same
// descriptions outside the judge. That subtracted half is not zero: a description's Run
// allocates roughly three per kind building its details map, stats or no stats — a
// pre-existing cost of the render contract, and not what this guard holds.
func TestJudgementAllocsStableAcrossKindCount(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; enforced in the non-race matrix")
	}
	const kinds = 8

	single := judgementAllocs(kinds)
	double := judgementAllocs(2 * kinds)

	t.Logf("traversal allocs/op: %d kinds = %.1f, %d kinds = %.1f", kinds, single, 2*kinds, double)
	assert.InDelta(t, single, double, 0, "the judgement path must not allocate per kind")
}
