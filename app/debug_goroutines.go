package app

import (
	"bufio"
	"net/http"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// handleGoroutines provides detailed goroutine information
func (d *DebugHandlers) handleGoroutines(c echo.Context) error {
	start := time.Now()

	// Query parameters
	includeStacks := c.QueryParam("stacks") == "true"
	detectLeaks := c.QueryParam("leaks") == "true"
	format := c.QueryParam("format") // "json" (default) or "text"

	if format == "text" {
		return d.handleGoroutinesText(c)
	}

	goroutineInfo, err := d.analyzeGoroutines(includeStacks, detectLeaks)
	if err != nil {
		resp := d.newDebugResponse(start, nil, err)
		return c.JSON(http.StatusInternalServerError, resp)
	}

	resp := d.newDebugResponse(start, goroutineInfo, nil)
	return c.JSON(http.StatusOK, resp)
}

// handleGoroutinesText returns raw goroutine stacks as text
func (d *DebugHandlers) handleGoroutinesText(c echo.Context) error {
	var buf strings.Builder
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 1); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to get goroutine dump")
	}

	c.Response().Header().Set("Content-Type", "text/plain")
	return c.String(http.StatusOK, buf.String())
}

// analyzeGoroutines performs comprehensive goroutine analysis
func (d *DebugHandlers) analyzeGoroutines(includeStacks, detectLeaks bool) (*GoroutineInfo, error) {
	info := &GoroutineInfo{
		Count:      runtime.NumGoroutine(),
		ByState:    make(map[string]int),
		ByFunction: make(map[string]int),
	}

	// Get goroutine profile
	var buf strings.Builder
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 1); err != nil {
		return nil, err
	}

	// Parse goroutine dump
	stacks, err := d.parseGoroutineDump(buf.String())
	if err != nil {
		return nil, err
	}

	// Analyze goroutines
	for _, stack := range stacks {
		info.ByState[stack.State]++
		info.ByFunction[stack.Function]++

		if includeStacks {
			info.Stacks = append(info.Stacks, stack)
		}
	}

	// Detect potential leaks if requested
	if detectLeaks {
		info.Leaks = d.detectPotentialLeaks(stacks)
	}

	return info, nil
}

// parseGoroutineDump parses the goroutine dump text into structured data
func (d *DebugHandlers) parseGoroutineDump(dump string) ([]GoroutineStack, error) {
	var stacks []GoroutineStack
	var currentStack *GoroutineStack

	scanner := bufio.NewScanner(strings.NewReader(dump))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "goroutine ") {
			currentStack = d.processGoroutineHeader(line, currentStack, &stacks)
			continue
		}

		d.processStackFrame(line, currentStack)
	}

	// Add final stack
	if currentStack != nil {
		stacks = append(stacks, *currentStack)
	}

	return stacks, scanner.Err()
}

// processGoroutineHeader processes a goroutine header line and creates a new stack
func (d *DebugHandlers) processGoroutineHeader(line string, currentStack *GoroutineStack, stacks *[]GoroutineStack) *GoroutineStack {
	// Save previous stack if exists
	if currentStack != nil {
		*stacks = append(*stacks, *currentStack)
	}

	newStack := d.parseGoroutineHeader(line)
	return newStack
}

// parseGoroutineHeader parses a goroutine header line: "goroutine 123 [running]:"
func (d *DebugHandlers) parseGoroutineHeader(line string) *GoroutineStack {
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return nil
	}

	id, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}

	state := strings.Trim(parts[2], "[]:")
	return &GoroutineStack{
		ID:    id,
		State: state,
		Stack: []string{},
	}
}

// processStackFrame processes a stack frame line and updates the current stack
func (d *DebugHandlers) processStackFrame(line string, currentStack *GoroutineStack) {
	if currentStack == nil {
		return
	}

	currentStack.Stack = append(currentStack.Stack, line)

	// Extract function name from first stack frame
	if currentStack.Function == "" && strings.Contains(line, "(") {
		if idx := strings.Index(line, "("); idx > 0 {
			currentStack.Function = strings.TrimSpace(line[:idx])
		}
	}
}

// detectPotentialLeaks identifies goroutines that might be leaked
func (d *DebugHandlers) detectPotentialLeaks(stacks []GoroutineStack) []PotentialLeak {
	var leaks []PotentialLeak
	functionCounts := d.countGoroutinesByFunction(stacks)

	for _, stack := range stacks {
		if leak := d.checkForLeak(stack, functionCounts); leak != nil {
			leaks = append(leaks, *leak)
		}
	}

	return leaks
}

// countGoroutinesByFunction counts how many goroutines are running each function
func (d *DebugHandlers) countGoroutinesByFunction(stacks []GoroutineStack) map[string]int {
	functionCounts := make(map[string]int)
	for _, stack := range stacks {
		functionCounts[stack.Function]++
	}
	return functionCounts
}

// checkForLeak checks if a single goroutine shows signs of being leaked
func (d *DebugHandlers) checkForLeak(stack GoroutineStack, functionCounts map[string]int) *PotentialLeak {
	if leak := d.checkHighFunctionCount(stack, functionCounts); leak != nil {
		return leak
	}
	if leak := d.checkChannelOperations(stack); leak != nil {
		return leak
	}
	if leak := d.checkSelectStatement(stack); leak != nil {
		return leak
	}
	if leak := d.checkNetworkIO(stack); leak != nil {
		return leak
	}
	return nil
}

// checkHighFunctionCount detects when too many goroutines run the same function
func (d *DebugHandlers) checkHighFunctionCount(stack GoroutineStack, functionCounts map[string]int) *PotentialLeak {
	if functionCounts[stack.Function] > 10 {
		return &PotentialLeak{
			ID:       stack.ID,
			Function: stack.Function,
			Reason:   "High count of goroutines with same function",
		}
	}
	return nil
}

// checkChannelOperations detects goroutines stuck on channel operations
func (d *DebugHandlers) checkChannelOperations(stack GoroutineStack) *PotentialLeak {
	if stack.State == "chan receive" || stack.State == "chan send" {
		return &PotentialLeak{
			ID:       stack.ID,
			Function: stack.Function,
			Reason:   "Potentially stuck on channel operation",
		}
	}
	return nil
}

// checkSelectStatement detects goroutines stuck in select statements
func (d *DebugHandlers) checkSelectStatement(stack GoroutineStack) *PotentialLeak {
	if stack.State == "select" {
		return &PotentialLeak{
			ID:       stack.ID,
			Function: stack.Function,
			Reason:   "Potentially stuck in select statement",
		}
	}
	return nil
}

// checkNetworkIO detects goroutines stuck on network IO operations
func (d *DebugHandlers) checkNetworkIO(stack GoroutineStack) *PotentialLeak {
	if stack.State != "IO wait" {
		return nil
	}

	for _, frame := range stack.Stack {
		if strings.Contains(frame, "net.") {
			return &PotentialLeak{
				ID:       stack.ID,
				Function: stack.Function,
				Reason:   "Potentially stuck on network IO",
			}
		}
	}
	return nil
}

// handleGC provides garbage collection information
func (d *DebugHandlers) handleGC(c echo.Context) error {
	start := time.Now()

	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	var gcStats debug.GCStats
	debug.ReadGCStats(&gcStats)

	gcInfo := &GCInfo{
		Stats:       gcStats,
		MemBefore:   memBefore.Alloc,
		HeapObjects: memBefore.HeapObjects,
		HeapSize:    memBefore.HeapSys,
		Forced:      false,
	}

	resp := d.newDebugResponse(start, gcInfo, nil)
	return c.JSON(http.StatusOK, resp)
}

// handleForceGC forces garbage collection and reports before/after memory
func (d *DebugHandlers) handleForceGC(c echo.Context) error {
	start := time.Now()

	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// Force GC
	runtime.GC()

	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	var gcStats debug.GCStats
	debug.ReadGCStats(&gcStats)

	gcInfo := &GCInfo{
		Stats:       gcStats,
		MemBefore:   memBefore.Alloc,
		MemAfter:    memAfter.Alloc,
		HeapObjects: memAfter.HeapObjects,
		HeapSize:    memAfter.HeapSys,
		Forced:      true,
	}

	resp := d.newDebugResponse(start, gcInfo, nil)
	return c.JSON(http.StatusOK, resp)
}