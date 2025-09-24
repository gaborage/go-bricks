package config

import (
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
		assert.Equal(t, `^[a-z0-9-]{1,64}$`, config.Pattern)
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
		assert.Equal(t, `^[a-z0-9-]{1,64}$`, config.Pattern)
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
		assert.Empty(t, config.Pattern)
	})

	t.Run("complex_valid_pattern", func(t *testing.T) {
		config := &IDValidationConfig{}
		pattern := `^[a-zA-Z][a-zA-Z0-9_-]{2,31}$`
		err := config.SetRegex(pattern)

		assert.NoError(t, err)
		assert.Equal(t, pattern, config.Pattern)

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
		assert.Equal(t, `^[0-9]+$`, config.Pattern)
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

	// Simulate concurrent access (basic test)
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()

			regex := config.GetRegex()
			if regex != nil {
				regex.MatchString("test")
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify state is still valid
	regex := config.GetRegex()
	assert.NotNil(t, regex)
	assert.True(t, regex.MatchString("abc"))
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
