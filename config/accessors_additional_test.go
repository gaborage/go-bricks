package config

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveFieldValueScenarios(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{"feature.flag": true})

	value, shouldSet, err := cfg.resolveFieldValue("feature.flag", false, "", false)
	require.NoError(t, err)
	assert.True(t, shouldSet)
	assert.Equal(t, true, value)

	_, _, err = cfg.resolveFieldValue("missing.key", true, "", false)
	require.Error(t, err)

	value, shouldSet, err = cfg.resolveFieldValue("missing.key", false, "fallback", true)
	require.NoError(t, err)
	assert.True(t, shouldSet)
	assert.Equal(t, "fallback", value)

	value, shouldSet, err = cfg.resolveFieldValue("missing.optional", false, "", false)
	require.NoError(t, err)
	assert.False(t, shouldSet)
	assert.Nil(t, value)
}

func TestSetFieldValueDurationBranching(t *testing.T) {
	cfg := setupTestConfig(t, map[string]any{"service.timeout": "150ms"})

	var target struct {
		Timeout time.Duration
	}

	field := reflect.ValueOf(&target).Elem().FieldByName("Timeout")

	err := cfg.setFieldValue(field, "service.timeout", false, "", false)
	require.NoError(t, err)
	assert.Equal(t, 150*time.Millisecond, target.Timeout)

	var defaultTarget struct {
		Timeout time.Duration
	}

	defaultField := reflect.ValueOf(&defaultTarget).Elem().FieldByName("Timeout")
	err = cfg.setFieldValue(defaultField, "service.missing", false, "1s", true)
	require.NoError(t, err)
	assert.Equal(t, time.Second, defaultTarget.Timeout)

	var skipTarget struct {
		Timeout time.Duration
	}

	skipField := reflect.ValueOf(&skipTarget).Elem().FieldByName("Timeout")
	err = cfg.setFieldValue(skipField, "service.not.there", false, "", false)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), skipTarget.Timeout)
}

func TestConvertHelpersAdditionalCoverage(t *testing.T) {
	cfg := setupTestConfig(t, nil)

	_, err := cfg.convertToString("   ", "empty.string", true)
	require.Error(t, err)

	result, err := cfg.convertToString(42, "non.string", false)
	require.NoError(t, err)
	assert.Equal(t, "42", result)

	_, err = cfg.convertToFloat64(struct{}{}, "invalid.float")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid float value")

	duration, err := cfg.convertToDuration("75ms", "duration.key")
	require.NoError(t, err)
	assert.Equal(t, 75*time.Millisecond, duration)

	duration, err = cfg.convertToDuration(time.Second, "duration.existing")
	require.NoError(t, err)
	assert.Equal(t, time.Second, duration)

	_, err = cfg.convertToDuration(123, "duration.invalid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported duration type")
}
