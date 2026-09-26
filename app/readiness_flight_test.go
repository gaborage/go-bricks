package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/server"
)

// judgmentWaiterFrame is the frame of a /ready request parked waiting on the judgment flight.
const judgmentWaiterFrame = "app.(*App).judgeReadiness("

// judgmentPanicSentinel is carried by a judgment's panic value; it must never reach a log
// line, a body or the returned error.
const judgmentPanicSentinel = "judgment-panic-sentinel-5c1e"

const readinessFailedMsg = "Readiness check failed"

// flightValueKey carries a request-scoped value a flight must inherit from its leader.
type flightValueKey struct{}

// readyResponse is one /ready answer, its body without the per-request time key.
type readyResponse struct {
	code int
	body map[string]any
	err  error
}

// judgmentFlightFixture is an App whose one critical kind holds every judgment until
// release, counting the judgments that reach it. Once released the kind reports failWith
// when set, else its judgment's own context error, so a judgment that inherited a caller's
// cancellation fails however the release races it.
type judgmentFlightFixture struct {
	app      *App
	cfg      *config.Config
	log      *recLogger
	calls    atomic.Int32
	release  func()
	failWith error
}

// newJudgmentFlightFixture builds the fixture; failWith is what the kind reports once
// released, nil for its judgment's own context error.
func newJudgmentFlightFixture(t *testing.T, failWith error) *judgmentFlightFixture {
	t.Helper()
	f := &judgmentFlightFixture{log: &recLogger{}, failWith: failWith}
	f.cfg = &config.Config{App: config.AppConfig{Name: testApp}}
	f.cfg.Server.Timeout.Middleware = time.Minute
	f.app = &App{cfg: f.cfg, logger: f.log}
	gate := make(chan struct{})
	f.release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(f.release)
	installSealedSlots(f.app, probeDescription{
		name:     componentDatabase,
		critical: true,
		live: func(ctx context.Context) error {
			f.calls.Add(1)
			<-gate
			if f.failWith != nil {
				return f.failWith
			}
			return ctx.Err()
		},
	})
	return f
}

// ready serves one /ready on ctx in the background.
func (f *judgmentFlightFixture) ready(ctx context.Context) <-chan readyResponse {
	done := make(chan readyResponse, 1)
	go func() {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, readyEndpoint, http.NoBody)
		res := readyResponse{err: f.app.readyCheck(server.NewHandlerContextForTest(w, req, f.cfg))}
		res.code = w.Code
		if w.Body.Len() > 0 {
			res.err = json.Unmarshal(w.Body.Bytes(), &res.body)
		}
		delete(res.body, timeKey)
		done <- res
	}()
	return done
}

// readyAll starts n background /ready requests on ctx.
func (f *judgmentFlightFixture) readyAll(ctx context.Context, n int) []<-chan readyResponse {
	responses := make([]<-chan readyResponse, n)
	for i := range responses {
		responses[i] = f.ready(ctx)
	}
	return responses
}

// awaitJudgmentInFlight waits until the leader's judgment reaches the held kind.
func (f *judgmentFlightFixture) awaitJudgmentInFlight(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 5*time.Second, time.Millisecond)
}

// failureLevels returns the level of every readiness-failure line logged so far.
func (f *judgmentFlightFixture) failureLevels() []string {
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	var levels []string
	for i := range f.log.events {
		if f.log.events[i].msg == readinessFailedMsg {
			levels = append(levels, f.log.events[i].level)
		}
	}
	return levels
}

// awaitJudgmentWaiters waits until n requests are parked on the judgment flight, so a
// release after it cannot outrun a request still on its way to join.
func awaitJudgmentWaiters(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return testutil.ParkedInSelect(judgmentWaiterFrame) == n }, 5*time.Second, time.Millisecond,
		"expected %d requests parked on the judgment flight", n)
}

// receiveReady returns a background request's answer, failing if it does not arrive.
func receiveReady(t *testing.T, done <-chan readyResponse) readyResponse {
	t.Helper()
	select {
	case res := <-done:
		require.NoError(t, res.err)
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("the /ready request did not answer")
		return readyResponse{}
	}
}

// requireAbandonedAnswer asserts the answer of a request that stopped waiting on the
// judgment: a 503 naming readiness itself, with no kind's name or statistics.
func requireAbandonedAnswer(t *testing.T, res readyResponse) {
	t.Helper()
	assert.Equal(t, http.StatusServiceUnavailable, res.code)
	assert.Equal(t, map[string]any{
		statusKey:          notReadyStatus,
		componentReadiness: unhealthyStatus,
		errorKey:           componentReadiness + " unavailable",
	}, res.body)
}

// TestReadyCheckCoalescesConcurrentJudgments pins the judgment flight (ADR-120): concurrent
// /ready requests share one in-flight judgment, so the kind is judged once and every request
// renders the same verdict. A finished judgment is never reused: the next request judges anew.
func TestReadyCheckCoalescesConcurrentJudgments(t *testing.T) {
	f := newJudgmentFlightFixture(t, nil)
	const requests = 8
	responses := f.readyAll(t.Context(), requests)
	awaitJudgmentWaiters(t, requests)
	f.release()

	first := receiveReady(t, responses[0])
	assert.Equal(t, http.StatusOK, first.code)
	assert.Equal(t, healthyStatus, first.body[componentDatabase])
	for _, done := range responses[1:] {
		res := receiveReady(t, done)
		assert.Equal(t, http.StatusOK, res.code)
		assert.Equal(t, first.body, res.body)
	}
	assert.Equal(t, int32(1), f.calls.Load(), "concurrent requests share one judgment")

	_, code := runReadyCheck(t, f.app, f.cfg)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, int32(2), f.calls.Load(), "a finished judgment is never reused")
	assert.Empty(t, f.failureLevels())
}

// TestReadyCheckLeaderCancellationFailsNoFollower pins the flight's detached context: the
// request that started the judgment walks away mid-flight and answers at once with the
// abandoned-request WARN, while the requests that joined its judgment still get the kind's
// verdict.
func TestReadyCheckLeaderCancellationFailsNoFollower(t *testing.T) {
	f := newJudgmentFlightFixture(t, nil)
	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	t.Cleanup(cancelLeader)
	leader := f.ready(leaderCtx)
	f.awaitJudgmentInFlight(t)
	const followers = 4
	responses := f.readyAll(t.Context(), followers)
	awaitJudgmentWaiters(t, followers+1)

	cancelLeader()
	requireAbandonedAnswer(t, receiveReady(t, leader))
	f.release()

	for _, done := range responses {
		assert.Equal(t, http.StatusOK, receiveReady(t, done).code)
	}
	assert.Equal(t, int32(1), f.calls.Load())
	assert.Equal(t, []string{"warn"}, f.failureLevels(), "only the abandoned leader logs, at WARN")
}

// TestReadyCheckCanceledFollowerLeavesTheJudgment pins the per-waiter select: a request that
// joined a judgment and is then abandoned answers while the judgment is still held, logging
// the abandoned-request WARN under readiness itself since no kind was judged for it, and the
// judgment runs on for the request that started it.
func TestReadyCheckCanceledFollowerLeavesTheJudgment(t *testing.T) {
	f := newJudgmentFlightFixture(t, nil)
	leader := f.ready(t.Context())
	f.awaitJudgmentInFlight(t)
	followerCtx, cancelFollower := context.WithCancel(t.Context())
	t.Cleanup(cancelFollower)
	follower := f.ready(followerCtx)
	awaitJudgmentWaiters(t, 2)

	cancelFollower()
	requireAbandonedAnswer(t, receiveReady(t, follower))
	event, ok := loggedEvent(f.log, readinessFailedMsg)
	require.True(t, ok)
	assert.Equal(t, "warn", event.level)
	assert.Equal(t, componentReadiness, event.str["component"])
	assert.Equal(t, context.Canceled.Error(), event.err)
	f.release()

	assert.Equal(t, http.StatusOK, receiveReady(t, leader).code)
	assert.Equal(t, []string{"warn"}, f.failureLevels())
}

// TestReadyCheckContainsAJudgmentPanic pins the flight's panic containment: a judgment that
// panics reaches readyCheck as an error naming the panic's type, which readyCheck returns to
// the engine's error handler without writing a body; the flight logs the panic once at ERROR
// with the stack of the panic site, the process survives DoChan's re-panic path, and the
// panic's value reaches neither the error nor any log line (ADR-081).
func TestReadyCheckContainsAJudgmentPanic(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: testApp}}
	log := &recLogger{}
	app := &App{cfg: cfg, logger: log}
	installSealedSlots(app, probeDescription{
		name:     componentDatabase,
		critical: true,
		live:     func(context.Context) error { panic("judgment failed: " + judgmentPanicSentinel) },
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, readyEndpoint, http.NoBody)
	err := app.readyCheck(server.NewHandlerContextForTest(w, req, cfg))

	require.EqualError(t, err, "readiness judgment panicked (type: string)")
	assert.Zero(t, w.Body.Len(), "the engine's error handler answers, not readyCheck")
	event, ok := loggedEvent(log, "Readiness judgment panicked")
	require.True(t, ok)
	assert.Equal(t, "error", event.level)
	assert.Equal(t, err.Error(), event.err)
	assert.Contains(t, event.str["stack"], "TestReadyCheckContainsAJudgmentPanic", "the stack is the panic site's")
	log.mu.Lock()
	defer log.mu.Unlock()
	for i := range log.events {
		assert.NotContains(t, fmt.Sprintf("%+v", log.events[i]), judgmentPanicSentinel)
	}
}

// TestReadyCheckCoalescedFailureFailsEveryRequest pins that a shared failing judgment fails
// every request that joined it: each answers the same 503 naming the blocking kind, and each
// logs its own failure line, as it did when every request judged on its own.
func TestReadyCheckCoalescedFailureFailsEveryRequest(t *testing.T) {
	f := newJudgmentFlightFixture(t, errors.New("db down"))
	const requests = 8
	responses := f.readyAll(t.Context(), requests)
	awaitJudgmentWaiters(t, requests)
	f.release()

	for _, done := range responses {
		res := receiveReady(t, done)
		assert.Equal(t, http.StatusServiceUnavailable, res.code)
		assert.Equal(t, map[string]any{
			statusKey:         notReadyStatus,
			componentDatabase: unhealthyStatus,
			errorKey:          componentDatabase + " unavailable",
		}, res.body)
	}
	assert.Equal(t, int32(1), f.calls.Load(), "concurrent requests share one judgment")
	assert.Equal(t, slices.Repeat([]string{"error"}, requests), f.failureLevels())
}

// TestReadyCheckLeaderDeadlineNamesTheBlockingKind pins that a request whose own deadline
// expires mid-judgment waits for the verdict rather than leaving with readiness itself: the
// flight ends with the leader's deadline, so a hung kind still reaches the ERROR line and the
// body by name, as it did when the request judged on its own.
func TestReadyCheckLeaderDeadlineNamesTheBlockingKind(t *testing.T) {
	const budget = 100 * time.Millisecond
	cfg := &config.Config{App: config.AppConfig{Name: testApp}}
	cfg.Server.Timeout.Middleware = budget
	log := &recLogger{}
	app := &App{cfg: cfg, logger: log}
	installSealedSlots(app, probeDescription{
		name:     componentDatabase,
		critical: true,
		live: func(ctx context.Context) error {
			<-ctx.Done()
			return fmt.Errorf("ping: %w", ctx.Err())
		},
	})
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, readyEndpoint, http.NoBody)
	require.NoError(t, app.readyCheck(server.NewHandlerContextForTest(w, req, cfg)))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, map[string]any{
		statusKey:         notReadyStatus,
		componentDatabase: unhealthyStatus,
		errorKey:          componentDatabase + " unavailable",
	}, body)
	event, ok := loggedEvent(log, readinessFailedMsg)
	require.True(t, ok)
	assert.Equal(t, "error", event.level)
	assert.Equal(t, componentDatabase, event.str["component"])
	assert.Contains(t, event.err, context.DeadlineExceeded.Error())
}

// TestReadinessFlightKeepsTheLeaderValues pins that the judgment flight inherits the leader's
// request-scoped values, a trace span among them: it detaches the leader's cancellation, not
// its context.
func TestReadinessFlightKeepsTheLeaderValues(t *testing.T) {
	app := &App{cfg: &config.Config{}, logger: &recLogger{}}
	var seen any
	installSealedSlots(app, probeDescription{
		name: componentDatabase,
		live: func(ctx context.Context) error {
			seen = ctx.Value(flightValueKey{})
			return nil
		},
	})

	_, err := app.judgeReadiness(context.WithValue(t.Context(), flightValueKey{}, "leader"))
	require.NoError(t, err)
	assert.Equal(t, "leader", seen)
}

// TestReadyCheckFlightIsPerApp pins the judgment flight's scope: a request to one App never
// joins another App's in-flight judgment.
func TestReadyCheckFlightIsPerApp(t *testing.T) {
	a := newJudgmentFlightFixture(t, nil)
	held := a.ready(t.Context())
	a.awaitJudgmentInFlight(t)
	b := newJudgmentFlightFixture(t, nil)
	b.release()

	assert.Equal(t, http.StatusOK, receiveReady(t, b.ready(t.Context())).code)
	assert.Equal(t, int32(1), b.calls.Load(), "the request ran its own App's judgment")
	a.release()
	assert.Equal(t, http.StatusOK, receiveReady(t, held).code)
}

// TestReadinessFlightBudget pins the judgment flight's context: it ends at the earlier of the
// leader's own deadline and server.timeout.middleware from the flight's start, the timeout
// counting only when it is positive, and it has no deadline when neither applies.
func TestReadinessFlightBudget(t *testing.T) {
	const (
		budget       = 30 * time.Second
		leaderBudget = 10 * time.Second
	)
	tests := []struct {
		name         string
		middleware   time.Duration
		leaderBudget time.Duration
		nilConfig    bool
		wantBudget   time.Duration
	}{
		{name: "middleware_timeout_bounds_the_flight", middleware: budget, leaderBudget: time.Hour, wantBudget: budget},
		{name: "earlier_leader_deadline_bounds_the_flight", middleware: budget, leaderBudget: leaderBudget, wantBudget: leaderBudget},
		{name: "leader_deadline_bounds_the_flight_without_a_timeout", leaderBudget: leaderBudget, wantBudget: leaderBudget},
		{name: "middleware_timeout_bounds_a_leader_without_a_deadline", middleware: budget, wantBudget: budget},
		{name: "zero_timeout_adds_no_deadline"},
		{name: "negative_timeout_adds_no_deadline", middleware: -time.Second},
		{name: "nil_config_adds_no_deadline", nilConfig: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{logger: &recLogger{}}
			if !tc.nilConfig {
				app.cfg = &config.Config{}
				app.cfg.Server.Timeout.Middleware = tc.middleware
			}
			var deadline time.Time
			var hasDeadline bool
			installSealedSlots(app, probeDescription{
				name: componentDatabase,
				live: func(ctx context.Context) error {
					deadline, hasDeadline = ctx.Deadline()
					return nil
				},
			})
			leaderCtx := t.Context()
			if tc.leaderBudget > 0 {
				var cancel context.CancelFunc
				leaderCtx, cancel = context.WithTimeout(leaderCtx, tc.leaderBudget)
				defer cancel()
			}

			start := time.Now()
			_, err := app.judgeReadiness(leaderCtx)
			require.NoError(t, err)

			require.Equal(t, tc.wantBudget > 0, hasDeadline)
			if tc.wantBudget > 0 {
				assert.WithinDuration(t, start.Add(tc.wantBudget), deadline, 5*time.Second)
			}
		})
	}
}
