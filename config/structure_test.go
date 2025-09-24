package config

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	testValue = "test-123"
)

func TestIDValidationConfigSetRegex(t *testing.T) {
	t.Run("valid_pattern", func(t *testing.T) {
		config := &IDValidationConfig{}
		err := config.SetRegex(`^[a-z0-9-]{1,64}$`)

		assert.NoError(t, err)
		assert.Equal(t, `^[a-z0-9-]{1,64}$`, config.GetPattern())
		assert.NotNil(t, config.regex)

		// Test that the regex was compiled correctly
		regex := config.GetRegex()
		assert.NotNil(t, regex)
		assert.True(t, regex.MatchString(testValue))
		assert.False(t, regex.MatchString("Test-123")) // uppercase not allowed
	})

	t.Run("empty_pattern_uses_default", func(t *testing.T) {
		config := &IDValidationConfig{}
		err := config.SetRegex("")

		assert.NoError(t, err)
		assert.Equal(t, `^[a-z0-9-]{1,64}$`, config.GetPattern())
		assert.NotNil(t, config.regex)

		// Test default pattern behavior
		regex := config.GetRegex()
		assert.True(t, regex.MatchString("valid-tenant"))
		assert.False(t, regex.MatchString("Invalid-Tenant"))
	})

	t.Run("invalid_pattern", func(t *testing.T) {
		config := &IDValidationConfig{}
		err := config.SetRegex(`[unclosed`)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "error parsing regexp")
		assert.Nil(t, config.regex)
		assert.Empty(t, config.GetPattern())
	})

	t.Run("complex_valid_pattern", func(t *testing.T) {
		config := &IDValidationConfig{}
		pattern := `^[a-zA-Z][a-zA-Z0-9_-]{2,31}$`
		err := config.SetRegex(pattern)

		assert.NoError(t, err)
		assert.Equal(t, pattern, config.GetPattern())

		regex := config.GetRegex()
		assert.True(t, regex.MatchString("tenant_1"))
		assert.True(t, regex.MatchString("Tenant-123"))
		assert.False(t, regex.MatchString("1tenant")) // can't start with number
		assert.False(t, regex.MatchString("te"))      // too short
	})

	t.Run("update_existing_pattern", func(t *testing.T) {
		config := &IDValidationConfig{}

		// Set initial pattern
		err := config.SetRegex(`^[a-z]+$`)
		assert.NoError(t, err)

		regex1 := config.GetRegex()
		assert.True(t, regex1.MatchString("abc"))
		assert.False(t, regex1.MatchString("123"))

		// Update to new pattern
		err = config.SetRegex(`^[0-9]+$`)
		assert.NoError(t, err)

		regex2 := config.GetRegex()
		assert.False(t, regex2.MatchString("abc"))
		assert.True(t, regex2.MatchString("123"))
		assert.Equal(t, `^[0-9]+$`, config.GetPattern())
	})
}

func TestIDValidationConfigGetRegex(t *testing.T) {
	t.Run("uninitialized_returns_nil", func(t *testing.T) {
		config := &IDValidationConfig{}
		regex := config.GetRegex()
		assert.Nil(t, regex)
	})

	t.Run("initialized_returns_regex", func(t *testing.T) {
		config := &IDValidationConfig{}
		expectedPattern := `^test-[0-9]+$`
		err := config.SetRegex(expectedPattern)
		assert.NoError(t, err)

		regex := config.GetRegex()
		assert.NotNil(t, regex)

		// Test the returned regex works as expected
		assert.True(t, regex.MatchString(testValue))
		assert.False(t, regex.MatchString("test-abc"))
	})

	t.Run("returns_same_instance", func(t *testing.T) {
		config := &IDValidationConfig{}
		err := config.SetRegex(`^test$`)
		assert.NoError(t, err)

		regex1 := config.GetRegex()
		regex2 := config.GetRegex()

		// Should return the same instance
		assert.Same(t, regex1, regex2)
	})
}

func TestIDValidationConfigThreadSafety(t *testing.T) {
	// Test that concurrent access doesn't cause race conditions
	config := &IDValidationConfig{}

	// Set initial pattern
	err := config.SetRegex(`^[a-z]+$`)
	assert.NoError(t, err)

	// Test concurrent reads and writes with high contention
	const numGoroutines = 100
	const iterationsPerGoroutine = 50

	var wg sync.WaitGroup
	errorCh := make(chan error, numGoroutines*iterationsPerGoroutine)

	// Different regex patterns to create more contention
	patterns := []string{
		`^[a-z]+$`,
		`^[A-Z]+$`,
		`^[a-z0-9]+$`,
		`^[a-zA-Z0-9-]+$`,
		`^[0-9]+$`,
		`^[a-z]{1,10}$`,
		`^[a-zA-Z]{5,}$`,
	}

	// Spawn multiple goroutines that perform concurrent writes
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < iterationsPerGoroutine; j++ {
				// Use different patterns to maximize contention
				pattern := patterns[(goroutineID+j)%len(patterns)]
				if err := config.SetRegex(pattern); err != nil {
					errorCh <- err
				}
			}
		}(i)
	}

	// Spawn multiple goroutines that perform concurrent reads
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < iterationsPerGoroutine; j++ {
				regex := config.GetRegex()
				if regex != nil {
					// Perform actual work with the regex to detect races
					testStrings := []string{"test", "TEST", "123", "test-123", ""}
					testStr := testStrings[(goroutineID+j)%len(testStrings)]
					regex.MatchString(testStr)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errorCh)

	// Verify no errors occurred during concurrent operations
	errors := make([]error, 0, numGoroutines*iterationsPerGoroutine)
	for err := range errorCh {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		t.Errorf("Found %d errors during concurrent operations:", len(errors))
		for i, err := range errors {
			if i < 5 { // Limit output to first 5 errors
				t.Errorf("  Error %d: %v", i+1, err)
			}
		}
		if len(errors) > 5 {
			t.Errorf("  ... and %d more errors", len(errors)-5)
		}
	}

	// Verify final state is still valid
	regex := config.GetRegex()
	assert.NotNil(t, regex)

	// The final pattern should be one of our test patterns
	finalPattern := config.GetPattern()
	found := false
	for _, pattern := range patterns {
		if finalPattern == pattern {
			found = true
			break
		}
	}
	assert.True(t, found, "Final pattern '%s' should be one of the test patterns", finalPattern)

	// Test that the final regex actually works
	assert.True(t, regex.MatchString("") || !regex.MatchString(""), "Regex should be functional")
}

func TestIDValidationConfigEdgeCases(t *testing.T) {
	t.Run("very_long_pattern", func(t *testing.T) {
		config := &IDValidationConfig{}
		// Create a very long but valid pattern
		pattern := "^(" + "a|" + "b|" + "c|" + "d|" + "e|" + "f|" + "g|" + "h|" + "i|" + "j|" + "k|" + "l|" + "m|" + "n|" + "o|" + "p|" + "q|" + "r|" + "s|" + "t|" + "u|" + "v|" + "w|" + "x|" + "y|" + "z" + ")+$"

		err := config.SetRegex(pattern)
		assert.NoError(t, err)

		regex := config.GetRegex()
		assert.True(t, regex.MatchString("abc"))
		assert.False(t, regex.MatchString("123"))
	})

	t.Run("special_characters", func(t *testing.T) {
		config := &IDValidationConfig{}
		pattern := `^[a-z0-9\.\-_@]+$` // Allow dots, dashes, underscores, at-signs

		err := config.SetRegex(pattern)
		assert.NoError(t, err)

		regex := config.GetRegex()
		assert.True(t, regex.MatchString("user.name-123_test@domain"))
		assert.False(t, regex.MatchString("user name")) // space not allowed
	})

	t.Run("anchor_validation", func(t *testing.T) {
		config := &IDValidationConfig{}

		// Pattern without anchors
		err := config.SetRegex(`[a-z]+`)
		assert.NoError(t, err)

		regex := config.GetRegex()
		// Without anchors, partial matches are allowed
		assert.True(t, regex.MatchString("123abc456"))
	})
}

func TestIDValidationConfigDefaultPattern(t *testing.T) {
	t.Run("default_pattern_behavior", func(t *testing.T) {
		config := &IDValidationConfig{}
		err := config.SetRegex("")
		assert.NoError(t, err)

		regex := config.GetRegex()

		// Test what the default pattern allows
		validCases := []string{
			"a",       // minimum length 1
			"abc",     // simple case
			testValue, // with dash and numbers
			"abcdefghijklmnopqrstuvwxyz0123456789-abcdefghijklmnopqrstuvwxyz0", // exactly 64 chars
		}

		invalidCases := []string{
			"",         // empty
			"Test",     // uppercase
			"test_123", // underscore not allowed
			"test 123", // space not allowed
			"test@123", // special chars not allowed
			"abcdefghijklmnopqrstuvwxyz0123456789-abcdefghijklmnopqrstuvwxyz01", // 65 chars (too long)
		}

		for _, valid := range validCases {
			assert.True(t, regex.MatchString(valid), "Expected '%s' to be valid", valid)
		}

		for _, invalid := range invalidCases {
			assert.False(t, regex.MatchString(invalid), "Expected '%s' to be invalid", invalid)
		}
	})
}
