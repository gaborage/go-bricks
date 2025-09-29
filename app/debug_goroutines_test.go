package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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
)

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
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/gc", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

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
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/gc", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

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
