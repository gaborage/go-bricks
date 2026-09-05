package testing

import (
	"crypto/rsa"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssertSecretAvailablePasses(t *testing.T) {
	m := NewMockKeyStore().WithSecret("mac", []byte("non-empty-secret"))
	AssertSecretAvailable(t, m, "mac")
}

// recordingT captures a helper's failure path without failing the test that observes it.
// FailNow does what *testing.T's does — runtime.Goexit — so the abort is OBSERVED rather
// than proxied by a counter: a shape that called Errorf here and aborted somewhere later
// would keep the count right while running statements the real helper never reaches.
// Callers must therefore drive it on its own goroutine; runAborting does that.
type recordingT struct {
	errors  []string
	failNow int
}

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordingT) FailNow() {
	r.failNow++
	runtime.Goexit()
}

var _ keyStoreReporter = (*recordingT)(nil)

// runAborting runs fn on its own goroutine and reports whether it returned normally.
// false means fn called FailNow: Goexit unwinds the goroutine, so the line after fn never
// executes while the deferred close still fires.
func runAborting(fn func()) (completed bool) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
		completed = true
	}()
	<-done
	return completed
}

// TestAssertKeyNotFoundAbortsOnFoundPublicKey pins ADR-101: an unexpectedly found public
// key must abort the caller's test at the first assertion, not record a failure and go on
// to judge the private key of a keystore already known to be in the wrong state.
func TestAssertKeyNotFoundAbortsOnFoundPublicKey(t *testing.T) {
	// The mock returns whatever pointer it was seeded with and never inspects it, so
	// presence in the map is the whole signal these assertions read — a real 2048-bit
	// key would cost ~70ms of keygen and prove nothing extra.
	found := &rsa.PublicKey{}

	tests := []struct {
		name          string
		ks            *MockKeyStore
		keyName       string
		wantCompleted bool
		wantFailNow   int
		wantErrSubstr string
	}{
		{
			name:          "found_public_key_aborts",
			ks:            NewMockKeyStore().WithPublicKey("leaked", found),
			keyName:       "leaked",
			wantCompleted: false,
			wantFailNow:   1,
			wantErrSubstr: `public key "leaked" should not be found`,
		},
		{
			name:          "absent_key_passes_both_assertions",
			ks:            NewMockKeyStore(),
			keyName:       "absent",
			wantCompleted: true,
			wantFailNow:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingT{}
			completed := runAborting(func() { assertKeyNotFound(rec, tt.ks, tt.keyName) })

			assert.Equal(t, tt.wantCompleted, completed,
				"a found public key must stop the helper, not merely record a failure")
			assert.Equal(t, tt.wantFailNow, rec.failNow,
				"FailNow distinguishes require (abort) from assert (continue)")
			if tt.wantErrSubstr == "" {
				assert.Empty(t, rec.errors, "an absent key must satisfy both lookups")
				return
			}
			require.NotEmpty(t, rec.errors)
			assert.Contains(t, rec.errors[0], tt.wantErrSubstr)
		})
	}
}
