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

// BenchmarkLogEventNonJSONFields guards the cost of the payload door on the
// path that must not pay for it: ordinary fields, driven through the REAL
// adapter doors a caller reaches.
//
// Measuring the door directly is not enough and once hid a regression: routing
// Bytes through FilterValue, whose parameter is `any`, boxed the slice header
// on every byte-slice field — an allocation a benchmark calling the door with a
// concrete []byte could never see. These drive log.Info().Str(...) and
// log.Info().Bytes(...) end to end, so a door that starts boxing shows up here.
func BenchmarkLogEventNonJSONFields(b *testing.B) {
	newLogger := func() *ZeroLogger {
		zl := zerolog.New(io.Discard)
		return &ZeroLogger{zlog: &zl, filter: NewSensitiveDataFilter(DefaultFilterConfig())}
	}

	b.Run("str", func(b *testing.B) {
		log := newLogger()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			log.Info().Str("detail", "user alice signed in").Msg("event")
		}
	})

	b.Run("bytes", func(b *testing.B) {
		log := newLogger()
		payload := []byte("not a json document at all")
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			log.Info().Bytes("body", payload).Msg("event")
		}
	})

	// The controls: the SAME doors on a logger with no filter at all. What the
	// payload door costs is the delta against these, not against an empty event
	// — zerolog's own Str and Bytes allocate on their own account.
	b.Run("control_str_unfiltered", func(b *testing.B) {
		zl := zerolog.New(io.Discard)
		log := &ZeroLogger{zlog: &zl}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			log.Info().Str("detail", "user alice signed in").Msg("event")
		}
	})

	b.Run("control_bytes_unfiltered", func(b *testing.B) {
		zl := zerolog.New(io.Discard)
		log := &ZeroLogger{zlog: &zl}
		payload := []byte("not a json document at all")
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			log.Info().Bytes("body", payload).Msg("event")
		}
	})

	b.Run("baseline_no_fields", func(b *testing.B) {
		log := newLogger()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			log.Info().Msg("event")
		}
	})
}

// BenchmarkFilterNonJSONString guards the cost of the payload door on the path
// that must not pay for it: an ordinary string field. The door's whole design
// rests on looksLikeJSON rejecting such a value before any parsing, so this
// benchmark reports 0 allocations — the same as the plain name check. A
// regression here means the door started touching every log line.
//
// The zero is exact, not approximate, and the `visited` map FilterValue makes on
// every call is not an exception to it: escape analysis keeps that map on the
// stack (`go build -gcflags=-m` reports "make(map[uintptr]struct {}) does not
// escape" at its construction), and an empty map allocates no buckets. If a
// future change makes it escape — storing it, passing it to something that
// outlives the call — this benchmark is where that shows up, as a non-zero
// baseline on the cheapest possible input.
func BenchmarkFilterNonJSONString(b *testing.B) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if filter.FilterValue("message", "user alice signed in") != any("user alice signed in") {
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
