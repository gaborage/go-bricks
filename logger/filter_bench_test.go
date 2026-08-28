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

// BenchmarkFilterNonJSONString guards the cost of the payload door on the path
// that must not pay for it: an ordinary string field. The door's whole design
// rests on looksLikeJSON rejecting such a value before any parsing, so this
// benchmark should report 0 allocations — the same as the plain name check.
// A regression here means the door started touching every log line.
func BenchmarkFilterNonJSONString(b *testing.B) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if filtered := filter.FilterValue("message", "user alice signed in"); filtered != any("user alice signed in") {
			b.Fatal("a non-JSON string must come back untouched")
		}
	}
}

// BenchmarkFilterNonJSONBytes is the same guard for the bytes door: a byte
// slice that does not open with a brace or bracket is returned untouched
// without allocating.
func BenchmarkFilterNonJSONBytes(b *testing.B) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())
	payload := []byte("not a json document at all")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		filterOpaquePayload(filter, payload, payload)
	}
}
