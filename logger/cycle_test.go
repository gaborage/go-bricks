package logger

import (
	"testing"
)

const (
	expectedMapMessage    = "Expected result to be a map"
	expectedLevel1Message = "Expected level to be 1, got '%v'"
	expectedNameMessage   = "Expected name to be '%s', got '%v'"
	expectedMaskedMessage = "Expected password to be masked, got '%v'"
	expectedFieldMessage  = "Expected '%s' field to be present"
)

// Helper function to validate basic map result structure
func validateMapResult(t *testing.T, result any) map[string]any {
	t.Helper()
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMapMessage)
	}
	return resultMap
}

// Helper function to validate name field in result
func validateNameField(t *testing.T, resultMap map[string]any, expectedName string) {
	t.Helper()
	if resultMap["name"] != expectedName {
		t.Errorf(expectedNameMessage, expectedName, resultMap["name"])
	}
}

// Helper function to validate password masking
func validatePasswordMasked(t *testing.T, resultMap map[string]any) {
	t.Helper()
	if resultMap["password"] != DefaultMaskValue {
		t.Errorf(expectedMaskedMessage, resultMap["password"])
	}
}

// Helper function to validate field presence
func validateFieldPresent(t *testing.T, resultMap map[string]any, fieldName string) {
	t.Helper()
	if _, exists := resultMap[fieldName]; !exists {
		t.Errorf(expectedFieldMessage, fieldName)
	}
}

// TestCycleDetection tests that the filter can handle self-referential data structures
func TestCycleDetection(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "secret"},
		MaskValue:       DefaultMaskValue,
	})

	t.Run("self_referential_struct", func(t *testing.T) {
		testSelfReferentialStruct(t, filter)
	})

	t.Run("mutually_referential_structs", func(t *testing.T) {
		testMutuallyReferentialStructs(t, filter)
	})

	t.Run("self_referential_slice", func(t *testing.T) {
		testSelfReferentialSlice(t, filter)
	})

	t.Run("cycle_with_sensitive_data", func(t *testing.T) {
		testCycleWithSensitiveData(t, filter)
	})
}

func testSelfReferentialStruct(t *testing.T, filter *SensitiveDataFilter) {
	type SelfRef struct {
		Name string   `json:"name"`
		Self *SelfRef `json:"self"`
	}

	// Create a self-referential structure
	obj := &SelfRef{Name: "root"}
	obj.Self = obj // Creates a cycle

	result := filter.FilterValue("data", obj)
	resultMap := validateMapResult(t, result)
	validateNameField(t, resultMap, "root")
	validateFieldPresent(t, resultMap, "self")
}

func testMutuallyReferentialStructs(t *testing.T, filter *SensitiveDataFilter) {
	type NodeA struct {
		Name string `json:"name"`
		B    any    `json:"b"`
	}

	type NodeB struct {
		Name string `json:"name"`
		A    *NodeA `json:"a"`
	}

	// Create mutually referential structures
	nodeA := &NodeA{Name: "A"}
	nodeB := &NodeB{Name: "B"}
	nodeA.B = nodeB
	nodeB.A = nodeA // Creates a cycle

	result := filter.FilterValue("data", nodeA)
	resultMap := validateMapResult(t, result)
	validateNameField(t, resultMap, "A")
}

func testSelfReferentialSlice(t *testing.T, filter *SensitiveDataFilter) {
	type SliceNode struct {
		Name     string       `json:"name"`
		Children []*SliceNode `json:"children"`
	}

	// Create a structure with a cycle in a slice
	parent := &SliceNode{Name: "parent"}
	child := &SliceNode{Name: "child"}
	parent.Children = []*SliceNode{child}
	child.Children = []*SliceNode{parent} // Creates a cycle

	result := filter.FilterValue("data", parent)
	resultMap := validateMapResult(t, result)
	validateNameField(t, resultMap, "parent")
}

func testCycleWithSensitiveData(t *testing.T, filter *SensitiveDataFilter) {
	type SecureNode struct {
		Name     string      `json:"name"`
		Password string      `json:"password"`
		Next     *SecureNode `json:"next"`
	}

	// Create a cycle with sensitive data
	node1 := &SecureNode{Name: "node1", Password: "secret1"}
	node2 := &SecureNode{Name: "node2", Password: "secret2"}
	node1.Next = node2
	node2.Next = node1 // Creates a cycle

	result := filter.FilterValue("data", node1)
	resultMap := validateMapResult(t, result)
	validateNameField(t, resultMap, "node1")
	validatePasswordMasked(t, resultMap)
}

// TestDepthLimiting tests that deep nesting doesn't cause stack overflow
func TestDepthLimiting(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password"},
		MaskValue:       DefaultMaskValue,
	})

	t.Run("deep_nesting_exceeds_limit", func(t *testing.T) {
		testDeepNestingExceedsLimit(t, filter)
	})

	t.Run("deeply_nested_maps", func(t *testing.T) {
		testDeeplyNestedMaps(t, filter)
	})

	t.Run("deeply_nested_slices", func(t *testing.T) {
		testDeeplyNestedSlices(t, filter)
	})
}

func testDeepNestingExceedsLimit(t *testing.T, filter *SensitiveDataFilter) {
	type DeepStruct struct {
		Level int         `json:"level"`
		Next  *DeepStruct `json:"next"`
	}

	// Create a deep structure that exceeds the default max depth
	var buildDeepStruct func(level int) *DeepStruct
	buildDeepStruct = func(level int) *DeepStruct {
		if level > DefaultMaxDepth+5 { // Go deeper than the limit
			return nil
		}
		return &DeepStruct{
			Level: level,
			Next:  buildDeepStruct(level + 1),
		}
	}

	deepStruct := buildDeepStruct(1)
	result := filter.FilterValue("data", deepStruct)
	resultMap := validateMapResult(t, result)

	if resultMap["level"] != 1 {
		t.Errorf(expectedLevel1Message, resultMap["level"])
	}
	validateFieldPresent(t, resultMap, "next")
}

func testDeeplyNestedMaps(t *testing.T, filter *SensitiveDataFilter) {
	// Create a deeply nested map structure
	var buildDeepMap func(level int) map[string]any
	buildDeepMap = func(level int) map[string]any {
		if level > DefaultMaxDepth+3 {
			return map[string]any{"leaf": true}
		}
		return map[string]any{
			"level": level,
			"next":  buildDeepMap(level + 1),
		}
	}

	deepMap := buildDeepMap(1)
	result := filter.FilterValue("data", deepMap)
	resultMap := validateMapResult(t, result)

	if resultMap["level"] != 1 {
		t.Errorf(expectedLevel1Message, resultMap["level"])
	}
}

func testDeeplyNestedSlices(t *testing.T, filter *SensitiveDataFilter) {
	type SliceNode struct {
		Level int          `json:"level"`
		Items []*SliceNode `json:"items"`
	}

	// Create deeply nested slice structure
	var buildDeepSlice func(level int) *SliceNode
	buildDeepSlice = func(level int) *SliceNode {
		if level > DefaultMaxDepth+3 {
			return &SliceNode{Level: level}
		}
		return &SliceNode{
			Level: level,
			Items: []*SliceNode{buildDeepSlice(level + 1)},
		}
	}

	deepSlice := buildDeepSlice(1)
	result := filter.FilterValue("data", deepSlice)
	resultMap := validateMapResult(t, result)

	if resultMap["level"] != 1 {
		t.Errorf(expectedLevel1Message, resultMap["level"])
	}
}

// TestCombinedCycleAndDepth tests edge cases where cycles and depth interact
func TestCombinedCycleAndDepth(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "secret"},
		MaskValue:       DefaultMaskValue,
	})

	t.Run("cycle_within_deep_structure", func(t *testing.T) {
		testCycleWithinDeepStructure(t, filter)
	})
}

func testCycleWithinDeepStructure(t *testing.T, filter *SensitiveDataFilter) {
	type ComplexNode struct {
		Name     string         `json:"name"`
		Password string         `json:"password"`
		Level    int            `json:"level"`
		Children []*ComplexNode `json:"children"`
		Parent   *ComplexNode   `json:"parent"`
	}

	// Build a tree structure
	root := &ComplexNode{Name: "root", Password: "root_secret", Level: 0}
	child1 := &ComplexNode{Name: "child1", Password: "child1_secret", Level: 1, Parent: root}
	child2 := &ComplexNode{Name: "child2", Password: "child2_secret", Level: 1, Parent: root}
	grandchild := &ComplexNode{Name: "grandchild", Password: "grand_secret", Level: 2, Parent: child1}

	root.Children = []*ComplexNode{child1, child2}
	child1.Children = []*ComplexNode{grandchild}

	// Create a cycle: grandchild points back to root
	grandchild.Children = []*ComplexNode{root}

	result := filter.FilterValue("data", root)
	resultMap := validateMapResult(t, result)

	validateNameField(t, resultMap, "root")
	validatePasswordMasked(t, resultMap)

	if resultMap["level"] != 0 {
		t.Errorf("Expected level to be 0, got '%v'", resultMap["level"])
	}
}

// TestEdgeCasesWithCycleDetection tests edge cases in cycle detection
func TestEdgeCasesWithCycleDetection(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password"},
		MaskValue:       DefaultMaskValue,
	})

	t.Run("nil_pointers_in_cycle_detection", func(t *testing.T) {
		testNilPointersInCycleDetection(t, filter)
	})

	t.Run("empty_visited_map_handling", func(t *testing.T) {
		testEmptyVisitedMapHandling(t, filter)
	})
}

func testNilPointersInCycleDetection(t *testing.T, filter *SensitiveDataFilter) {
	type NodeWithNil struct {
		Name string       `json:"name"`
		Next *NodeWithNil `json:"next"`
	}

	// Create structure with nil in the chain
	node1 := &NodeWithNil{Name: "node1"}
	node2 := &NodeWithNil{Name: "node2", Next: nil}
	node1.Next = node2

	result := filter.FilterValue("data", node1)
	resultMap := validateMapResult(t, result)
	validateNameField(t, resultMap, "node1")
}

func testEmptyVisitedMapHandling(t *testing.T, filter *SensitiveDataFilter) {
	type SimpleStruct struct {
		Name string `json:"name"`
	}

	simple := &SimpleStruct{Name: "simple"}

	// This should work with fresh visited map
	result := filter.FilterValue("data", simple)
	resultMap := validateMapResult(t, result)
	validateNameField(t, resultMap, "simple")
}
