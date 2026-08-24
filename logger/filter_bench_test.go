package logger

import (
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"
)

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

// BenchmarkLogEventAdapterErr measures the Err seam with and without a redactor
// configured, so the cost the hook adds to a hot path stays visible.
func BenchmarkLogEventAdapterErr(b *testing.B) {
	err := errors.New("benchmark error")

	run := func(b *testing.B, config *FilterConfig) {
		zl := zerolog.New(io.Discard)
		log := &ZeroLogger{zlog: &zl, filter: NewSensitiveDataFilter(config)}
		for b.Loop() {
			log.Error().Err(err).Msg("boom")
		}
	}

	b.Run("no_redactor", func(b *testing.B) { run(b, DefaultFilterConfig()) })

	b.Run("with_redactor", func(b *testing.B) {
		config := DefaultFilterConfig()
		config.ErrorRedactor = func(err error) string { return err.Error() }
		run(b, config)
	})
}
