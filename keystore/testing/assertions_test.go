package testing

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"runtime"
	"strings"
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

// stubKeyStore returns a key and an error TOGETHER, the contract violation MockKeyStore
// cannot express: its lookups are map hits, so a miss can never carry a key.
type stubKeyStore struct {
	pub     *rsa.PublicKey
	pubErr  error
	priv    *rsa.PrivateKey
	privErr error
}

func (s *stubKeyStore) PublicKey(string) (*rsa.PublicKey, error)   { return s.pub, s.pubErr }
func (s *stubKeyStore) PrivateKey(string) (*rsa.PrivateKey, error) { return s.priv, s.privErr }
func (s *stubKeyStore) Secret(string) ([]byte, error)              { return nil, errNotFound }

var errNotFound = errors.New("key not found")

// keyMaterialDigits matches a digit run long enough to be a rendered modulus, exponent or
// private exponent: Go prints a big.Int field in decimal, so any default rendering of the
// fixtures below (%v, %+v, %#v) carries hundreds of digits, while a %T type name carries
// none and an index, length or line number in these messages never exceeds four. That is
// exactly what the guard catches — Go's default NUMERIC rendering of a key's big.Int
// fields — and no more: a base64, hex or PEM encoding of the same material is not a long
// digit run and would slip past. The NotContains check alongside it covers the other half
// of the realistic leak, Go's pointer-to-struct render shape, whatever the field values.
var keyMaterialDigits = regexp.MustCompile(`\d{5,}`)

// TestAssertKeyNotFoundRejectsReturnedKey pins the second half of the miss contract: an
// error alone does not prove absence, so a key handed back ALONGSIDE the error must still
// fail — reported by type, never by value.
func TestAssertKeyNotFoundRejectsReturnedKey(t *testing.T) {
	// The fixtures carry SYNTHETIC digit-bearing material — a 2049-bit power of two and
	// the usual public exponent — so the anti-leak assertions below are load-bearing: a
	// zero-value key renders as "&{<nil> 0}", which matches no long digit run however
	// badly the helper leaks. It stays synthetic rather than a real rsa.GenerateKey pair
	// for two reasons: NotRegexp prints its whole subject on failure, so a REAL key would
	// be dumped into the log by the very assertion guarding against that, and generating
	// one costs ~70ms while proving nothing extra — the helper only tests these pointers
	// against nil and prints their type.
	strayModulus := func() *big.Int { return new(big.Int).Lsh(big.NewInt(1), 2048) }

	tests := []struct {
		name          string
		ks            *stubKeyStore
		wantCompleted bool
		wantFailNow   int
		// wantErrCount is not redundant with wantErrSubstr: it pins that the private arm
		// records EXACTLY one error, not two.
		wantErrCount  int
		wantErrSubstr string
	}{
		{
			name:          "public_key_returned_with_error_aborts",
			ks:            &stubKeyStore{pub: &rsa.PublicKey{N: strayModulus(), E: 65537}, pubErr: errNotFound, privErr: errNotFound},
			wantCompleted: false,
			wantFailNow:   1,
			wantErrCount:  1,
			wantErrSubstr: "*rsa.PublicKey",
		},
		{
			name:          "private_key_returned_with_error_records",
			ks:            &stubKeyStore{pubErr: errNotFound, priv: &rsa.PrivateKey{D: strayModulus()}, privErr: errNotFound},
			wantCompleted: true,
			wantFailNow:   0,
			wantErrCount:  1,
			wantErrSubstr: "*rsa.PrivateKey",
		},
		{
			name:          "no_keys_returned_passes",
			ks:            &stubKeyStore{pubErr: errNotFound, privErr: errNotFound},
			wantCompleted: true,
			wantFailNow:   0,
			wantErrCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingT{}
			completed := runAborting(func() { assertKeyNotFound(rec, tt.ks, "stray") })

			assert.Equal(t, tt.wantCompleted, completed,
				"a returned public key must stop the helper, a returned private key must not")
			assert.Equal(t, tt.wantFailNow, rec.failNow,
				"FailNow distinguishes require (abort) from assert (continue)")
			require.Len(t, rec.errors, tt.wantErrCount)

			joined := strings.Join(rec.errors, "\n")
			if tt.wantErrSubstr != "" {
				assert.Contains(t, joined, tt.wantErrSubstr, "the stray key is named by type")
				assert.Contains(t, joined, `"stray"`, "the failure must name the key looked up")
			}
			assert.NotRegexp(t, keyMaterialDigits, joined,
				"a long digit run means a key VALUE reached the test log")
			assert.NotContains(t, joined, "&{",
				"Go's pointer-to-struct render shape means a key VALUE reached the test log")
		})
	}
}
