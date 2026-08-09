package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

const (
	chanReceive       = "chan receive"
	ioWait            = "IO wait"
	mainFunctionRef   = "main.main"
	workerLoopRef     = "worker.loop"
	chanSendRef       = "chan send"
	netListenRef      = "net.listen"
	netTCPListenerRef = "net.(*TCPListener).Accept"
	testFuncRef       = "test.func"
	runningStateRef   = "running"
	sleepStateRef     = "sleep"
)

// debug2GoroutineDump was captured on go1.26.5 darwin/arm64 with:
//
//	mkdir -p /tmp/grcap && cd /tmp/grcap    # write main.go from plan 102 Step 2
//	GOWORK=off go run main.go > /tmp/grcap/out.txt
//
// The three blocks below are copied verbatim (byte-for-byte, including tab
// indentation) from the DEBUG2 half of that capture: one goroutine per state
// (running, chan receive, sleep).
const debug2GoroutineDump = `goroutine 1 [running]:
runtime/pprof.writeGoroutineStacks({0x100e2a738, 0x307c2d710018})
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/runtime/pprof/pprof.go:819 +0x6c
runtime/pprof.writeGoroutine({0x100e2a738?, 0x307c2d710018?}, 0x0?)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/runtime/pprof/pprof.go:782 +0x2c
runtime/pprof.(*Profile).WriteTo(0x100e2a738?, {0x100e2a738?, 0x307c2d710018?}, 0x1?)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/runtime/pprof/pprof.go:408 +0x144
main.main()
	/tmp/grcap/main.go:18 +0xe8

goroutine 35 [chan receive]:
main.main.func1()
	/tmp/grcap/main.go:12 +0x24
created by main.main in goroutine 1
	/tmp/grcap/main.go:12 +0x6c

goroutine 36 [sleep]:
time.Sleep(0x34630b8a000)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/runtime/time.go:363 +0x150
main.main.func2()
	/tmp/grcap/main.go:13 +0x28
created by main.main in goroutine 1
	/tmp/grcap/main.go:13 +0x78
`

// debug1GoroutineDump was captured in the same run (see debug2GoroutineDump
// provenance above) from the DEBUG1 half: the aggregated pprof profile header
// plus its first two "#\t0x…" frame lines, copied verbatim.
const debug1GoroutineDump = `goroutine profile: total 3
1 @ 0x100ca6a8c 0x100cdeef4 0x100d216f4 0x100d21520 0x100d1ed14 0x100d2f478 0x100cafe54 0x100ce6264
#	0x100d216f3	runtime/pprof.writeRuntimeProfile+0xb3	/opt/homebrew/Cellar/go/1.26.5/libexec/src/runtime/pprof/pprof.go:851
#	0x100d2151f	runtime/pprof.writeGoroutine+0x4f	/opt/homebrew/Cellar/go/1.26.5/libexec/src/runtime/pprof/pprof.go:784
`

func TestNormalizeGoroutineState(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple running state",
			input:    "running",
			expected: "running",
		},
		{
			name:     "composite state with annotations",
			input:    "select [chan receive, locked to thread]",
			expected: "select",
		},
		{
			name:     "chan receive with annotations",
			input:    "chan receive [locked to thread]",
			expected: chanReceive,
		},
		{
			name:     "IO wait state",
			input:    ioWait,
			expected: ioWait,
		},
		{
			name:     "empty state becomes unknown",
			input:    "",
			expected: unknownStatus,
		},
		{
			name:     "whitespace only becomes unknown",
			input:    "   ",
			expected: unknownStatus,
		},
		{
			name:     "complex composite state",
			input:    "select [chan receive, chan send, locked to thread]",
			expected: "select",
		},
		{
			name:     "state with brackets but primary after",
			input:    "[annotations] primary",
			expected: "primary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := debugHandlers.normalizeGoroutineState(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetectPotentialLeaks(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	tests := []struct {
		name     string
		stacks   []GoroutineStack
		expected int // number of leaks detected
	}{
		{
			name: "no leaks",
			stacks: []GoroutineStack{
				{ID: 1, State: "running", Function: mainFunctionRef},
				{ID: 2, State: "running", Function: "runtime.main"},
			},
			expected: 0,
		},
		{
			name: "high function count leak",
			stacks: func() []GoroutineStack {
				var stacks []GoroutineStack
				// Create 12 goroutines with same function (>10 threshold)
				for i := 1; i <= 12; i++ {
					stacks = append(stacks, GoroutineStack{
						ID:       i,
						State:    "running",
						Function: workerLoopRef,
					})
				}
				return stacks
			}(),
			expected: 12, // All goroutines with same function are flagged
		},
		{
			name: "channel operation leaks",
			stacks: []GoroutineStack{
				{ID: 1, State: chanReceive, Function: "worker.receive"},
				{ID: 2, State: chanSendRef, Function: "worker.send"},
				{ID: 3, State: "running", Function: mainFunctionRef},
			},
			expected: 2, // Two channel operations
		},
		{
			name: "select statement leak",
			stacks: []GoroutineStack{
				{ID: 1, State: "select", Function: "worker.select"},
				{ID: 2, State: "running", Function: mainFunctionRef},
			},
			expected: 1, // One select statement
		},
		{
			name: "network IO leak",
			stacks: []GoroutineStack{
				{
					ID:       1,
					State:    ioWait,
					Function: netListenRef,
					Stack:    []string{netTCPListenerRef, "main.server"},
				},
				{ID: 2, State: "running", Function: mainFunctionRef},
			},
			expected: 1, // One network IO wait
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaks := debugHandlers.detectPotentialLeaks(tt.stacks)
			assert.Len(t, leaks, tt.expected)
		})
	}
}

func TestCountGoroutinesByFunction(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	stacks := []GoroutineStack{
		{Function: mainFunctionRef},
		{Function: workerLoopRef},
		{Function: workerLoopRef},
		{Function: netListenRef},
	}

	counts := debugHandlers.countGoroutinesByFunction(stacks)

	expected := map[string]int{
		mainFunctionRef: 1,
		workerLoopRef:   2,
		netListenRef:    1,
	}

	assert.Equal(t, expected, counts)
}

func TestCheckForLeak(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	functionCounts := map[string]int{
		workerLoopRef:   15, // Above threshold
		mainFunctionRef: 1,
	}

	tests := []struct {
		name     string
		stack    GoroutineStack
		expected bool // whether leak is detected
	}{
		{
			name: "high function count",
			stack: GoroutineStack{
				ID:       1,
				Function: workerLoopRef,
				State:    "running",
			},
			expected: true,
		},
		{
			name: "channel receive",
			stack: GoroutineStack{
				ID:       2,
				Function: "worker.receive",
				State:    chanReceive,
			},
			expected: true,
		},
		{
			name: "select statement",
			stack: GoroutineStack{
				ID:       3,
				Function: "worker.select",
				State:    "select",
			},
			expected: true,
		},
		{
			name: "network IO",
			stack: GoroutineStack{
				ID:       4,
				Function: netListenRef,
				State:    ioWait,
				Stack:    []string{netTCPListenerRef},
			},
			expected: true,
		},
		{
			name: "normal running goroutine",
			stack: GoroutineStack{
				ID:       5,
				Function: mainFunctionRef,
				State:    "running",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leak := debugHandlers.checkForLeak(tt.stack, functionCounts)
			if tt.expected {
				assert.NotNil(t, leak)
				assert.Equal(t, tt.stack.ID, leak.ID)
				assert.Equal(t, tt.stack.Function, leak.Function)
			} else {
				assert.Nil(t, leak)
			}
		})
	}
}

func TestCheckHighFunctionCount(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	tests := []struct {
		name           string
		stack          GoroutineStack
		functionCounts map[string]int
		expectedLeak   bool
	}{
		{
			name: "above threshold",
			stack: GoroutineStack{
				ID:       1,
				Function: workerLoopRef,
			},
			functionCounts: map[string]int{workerLoopRef: 15},
			expectedLeak:   true,
		},
		{
			name: "at threshold",
			stack: GoroutineStack{
				ID:       2,
				Function: workerLoopRef,
			},
			functionCounts: map[string]int{workerLoopRef: 10},
			expectedLeak:   false,
		},
		{
			name: "below threshold",
			stack: GoroutineStack{
				ID:       3,
				Function: mainFunctionRef,
			},
			functionCounts: map[string]int{mainFunctionRef: 1},
			expectedLeak:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leak := debugHandlers.checkHighFunctionCount(tt.stack, tt.functionCounts)
			if tt.expectedLeak {
				assert.NotNil(t, leak)
				assert.Contains(t, leak.Reason, "High count")
			} else {
				assert.Nil(t, leak)
			}
		})
	}
}

func TestCheckChannelOperations(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	tests := []struct {
		name         string
		state        string
		expectedLeak bool
	}{
		{
			name:         chanReceive,
			state:        chanReceive,
			expectedLeak: true,
		},
		{
			name:         chanSendRef,
			state:        chanSendRef,
			expectedLeak: true,
		},
		{
			name:         "running",
			state:        "running",
			expectedLeak: false,
		},
		{
			name:         "select",
			state:        "select",
			expectedLeak: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := GoroutineStack{
				ID:       1,
				Function: testFuncRef,
				State:    tt.state,
			}
			leak := debugHandlers.checkChannelOperations(stack)
			if tt.expectedLeak {
				assert.NotNil(t, leak)
				assert.Contains(t, leak.Reason, "channel operation")
			} else {
				assert.Nil(t, leak)
			}
		})
	}
}

func TestCheckSelectStatement(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	tests := []struct {
		name         string
		state        string
		expectedLeak bool
	}{
		{
			name:         "select state",
			state:        "select",
			expectedLeak: true,
		},
		{
			name:         "running state",
			state:        "running",
			expectedLeak: false,
		},
		{
			name:         "chan receive state",
			state:        chanReceive,
			expectedLeak: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := GoroutineStack{
				ID:       1,
				Function: testFuncRef,
				State:    tt.state,
			}
			leak := debugHandlers.checkSelectStatement(stack)
			if tt.expectedLeak {
				assert.NotNil(t, leak)
				assert.Contains(t, leak.Reason, "select statement")
			} else {
				assert.Nil(t, leak)
			}
		})
	}
}

func TestCheckNetworkIO(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	tests := []struct {
		name         string
		state        string
		stack        []string
		expectedLeak bool
	}{
		{
			name:         "network IO wait",
			state:        ioWait,
			stack:        []string{netTCPListenerRef, "main.server"},
			expectedLeak: true,
		},
		{
			name:         "IO wait without network",
			state:        ioWait,
			stack:        []string{"os.(*File).Read", "main.readFile"},
			expectedLeak: false,
		},
		{
			name:         "not IO wait",
			state:        "running",
			stack:        []string{netTCPListenerRef},
			expectedLeak: false,
		},
		{
			name:         "IO wait empty stack",
			state:        ioWait,
			stack:        []string{},
			expectedLeak: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := GoroutineStack{
				ID:       1,
				Function: testFuncRef,
				State:    tt.state,
				Stack:    tt.stack,
			}
			leak := debugHandlers.checkNetworkIO(stack)
			if tt.expectedLeak {
				assert.NotNil(t, leak)
				assert.Contains(t, leak.Reason, "network IO")
			} else {
				assert.Nil(t, leak)
			}
		})
	}
}

func TestHandleGC(t *testing.T) {
	// Create test app and handlers
	app := &App{
		logger: logger.New("info", false),
	}
	debugConfig := &config.DebugConfig{
		Enabled:    true,
		PathPrefix: "/_debug",
	}
	debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

	// Create test request
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/gc", http.NoBody)
	rec := httptest.NewRecorder()
	c := server.NewHandlerContextForTest(rec, req, nil)

	// Execute handler
	err := debugHandlers.handleGC(c)

	// Verify response
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "mem_before")
	assert.Contains(t, rec.Body.String(), "heap_objects")
	assert.Contains(t, rec.Body.String(), "forced")
	assert.Contains(t, rec.Body.String(), `"forced":false`)
}

func TestHandleForceGC(t *testing.T) {
	// Create test app and handlers
	app := &App{
		logger: logger.New("info", false),
	}
	debugConfig := &config.DebugConfig{
		Enabled:    true,
		PathPrefix: "/_debug",
	}
	debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

	// Create test request
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/gc", http.NoBody)
	rec := httptest.NewRecorder()
	c := server.NewHandlerContextForTest(rec, req, nil)

	// Execute handler
	err := debugHandlers.handleForceGC(c)

	// Verify response
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "mem_before")
	assert.Contains(t, rec.Body.String(), "mem_after")
	assert.Contains(t, rec.Body.String(), "heap_objects")
	assert.Contains(t, rec.Body.String(), "forced")
	assert.Contains(t, rec.Body.String(), `"forced":true`)
}

func TestParseGoroutineDumpParsesDebug2Format(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	stacks, err := debugHandlers.parseGoroutineDump(debug2GoroutineDump)
	require.NoError(t, err)
	require.Len(t, stacks, 3)

	assert.Equal(t, 1, stacks[0].ID)
	assert.Equal(t, runningStateRef, stacks[0].State)
	assert.Equal(t, "runtime/pprof.writeGoroutineStacks", stacks[0].Function)
	assert.NotEmpty(t, stacks[0].Stack)

	assert.Equal(t, 35, stacks[1].ID)
	assert.Equal(t, chanReceive, stacks[1].State)
	assert.Equal(t, "main.main.func1", stacks[1].Function)
	assert.NotEmpty(t, stacks[1].Stack)

	assert.Equal(t, 36, stacks[2].ID)
	assert.Equal(t, sleepStateRef, stacks[2].State)
	assert.Equal(t, "time.Sleep", stacks[2].Function)
	assert.NotEmpty(t, stacks[2].Stack)
}

func TestAnalyzeGoroutinesPopulatesAggregatesFromLiveDump(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	info, err := debugHandlers.analyzeGoroutines(true, false)
	require.NoError(t, err)
	require.NotNil(t, info)

	require.NotEmpty(t, info.Stacks)
	assert.NotEmpty(t, info.ByState)
	assert.NotEmpty(t, info.ByFunction)

	foundValidStack := false
	for _, stack := range info.Stacks {
		if stack.ID > 0 && stack.Function != "" {
			foundValidStack = true
			break
		}
	}
	assert.True(t, foundValidStack, "expected at least one stack with a positive ID and non-empty Function")

	assert.Positive(t, info.Count)
}

func TestParseGoroutineDumpIgnoresDebug1Profile(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	stacks, err := debugHandlers.parseGoroutineDump(debug1GoroutineDump)
	require.NoError(t, err)
	assert.Empty(t, stacks)
}
