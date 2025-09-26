package tracking

import (
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
)

func TestNewSettingsDefaultsWhenConfigNil(t *testing.T) {
	settings := NewSettings(nil)

	if settings.slowQueryThreshold != DefaultSlowQueryThreshold {
		t.Fatalf("expected default slow query threshold, got %v", settings.slowQueryThreshold)
	}
	if settings.maxQueryLength != DefaultMaxQueryLength {
		t.Fatalf("expected default max query length, got %d", settings.maxQueryLength)
	}
	if settings.logQueryParameters {
		t.Fatalf("expected parameters logging disabled by default")
	}
}

func TestNewSettingsUsesPositiveOverrides(t *testing.T) {
	cfg := &config.DatabaseConfig{}
	cfg.Query.Slow.Threshold = 5 * time.Second
	cfg.Query.Log.MaxLength = 123
	cfg.Query.Log.Parameters = true

	settings := NewSettings(cfg)

	if settings.slowQueryThreshold != 5*time.Second {
		t.Fatalf("expected slow query threshold override, got %v", settings.slowQueryThreshold)
	}
	if settings.maxQueryLength != 123 {
		t.Fatalf("expected max length override, got %d", settings.maxQueryLength)
	}
	if !settings.logQueryParameters {
		t.Fatalf("expected parameters logging enabled")
	}
}

func TestNewSettingsIgnoresNonPositiveOverrides(t *testing.T) {
	cfg := &config.DatabaseConfig{}
	cfg.Query.Slow.Threshold = 0
	cfg.Query.Log.MaxLength = -10

	settings := NewSettings(cfg)

	if settings.slowQueryThreshold != DefaultSlowQueryThreshold {
		t.Fatalf("expected default slow query threshold, got %v", settings.slowQueryThreshold)
	}
	if settings.maxQueryLength != DefaultMaxQueryLength {
		t.Fatalf("expected default max query length, got %d", settings.maxQueryLength)
	}
}

func TestSettingsAccessorsReturnStoredValues(t *testing.T) {
	settings := Settings{
		slowQueryThreshold: 7 * time.Second,
		maxQueryLength:     42,
		logQueryParameters: true,
	}

	if settings.SlowQueryThreshold() != 7*time.Second {
		t.Fatalf("unexpected SlowQueryThreshold value: %v", settings.SlowQueryThreshold())
	}
	if settings.MaxQueryLength() != 42 {
		t.Fatalf("unexpected MaxQueryLength value: %d", settings.MaxQueryLength())
	}
	if !settings.LogQueryParameters() {
		t.Fatalf("expected LogQueryParameters to be true")
	}
}
