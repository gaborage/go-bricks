package logger

import "testing"

func BenchmarkIsSensitiveField(b *testing.B) {
	// Mixed-case needles mirror app.resolveLoggerFilterConfig, which appends
	// consumer YAML entries with their original case preserved.
	cfg := DefaultFilterConfig()
	cfg.SensitiveFields = append(cfg.SensitiveFields, "PAN", "CVV2", "OTP")
	f := NewSensitiveDataFilter(cfg)
	for b.Loop() {
		f.isSensitiveField("request_duration_ms")
	}
}
