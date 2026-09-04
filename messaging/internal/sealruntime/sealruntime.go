// Package sealruntime is the link-time seam between the messaging package and the
// payload-sealing codec (ADR-097). It mirrors internal/streamruntime (ADR-091): the
// codec registers itself from messaging/sealed's init, the app configures the runtime
// facts once at bootstrap, and messaging reads both without importing jose or the
// keystore — so a process that never imports messaging/sealed carries no sealing code.
// The app already links go-jose through HTTP jose; the import gate keeps `messaging`
// itself jose-free.
package sealruntime

import (
	"context"
	"crypto/rsa"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// ErrNotLinked fires at startup when a seal-tagged type is declared but the codec was
// never linked into the build.
var ErrNotLinked = errors.New(
	`a seal-tagged event type is declared but the sealing codec is not linked; import _ "github.com/gaborage/go-bricks/messaging/sealed"`)

// ErrNotConfigured fires when a seal-tagged type is declared before the app configured
// the sealing runtime — a wiring order mistake, never a per-message condition.
var ErrNotConfigured = errors.New("messaging: sealing runtime not configured before declarations were collected")

// ErrKeyStoreMissing fires when a seal-tagged type is declared and no keystore module is
// registered: key material is the sole trust act, so there is nothing to seal with.
var ErrKeyStoreMissing = errors.New("messaging: a seal-tagged event type needs key material; register keystore.NewModule() before the declaring module")

// Tenancy is the consume-side tenancy fact the opener's tid rule depends on (#1309 G2/G10).
type Tenancy uint8

const (
	// TenancyDisabled is multitenant.enabled: false — no tid rule, value surfaced only.
	TenancyDisabled Tenancy = iota
	// TenancyShared is multitenant.enabled + messaging.tenancy: shared — tid required
	// unless the consumer declares TenantOptional, and equality-checked against the carrier.
	TenancyShared
	// TenancyPerTenant is multitenant.enabled + per-tenant messaging — present-and-different
	// from the context tenant is poison, absent is accepted.
	TenancyPerTenant
)

// String names the tenancy for errors and logs.
func (t Tenancy) String() string {
	switch t {
	case TenancyDisabled:
		return "disabled"
	case TenancyShared:
		return "shared"
	case TenancyPerTenant:
		return "per-tenant"
	default:
		return "unknown"
	}
}

// KeyStore is the app.KeyStore subset sealing needs: stdlib RSA types only, so this seam
// names nothing from jose or keystore. The codec type-asserts the value for the family
// enumeration the keystore also implements.
type KeyStore interface {
	PublicKey(name string) (*rsa.PublicKey, error)
	PrivateKey(name string) (*rsa.PrivateKey, error)
}

// Runtime is what the app knows and the codec needs, fixed once at bootstrap.
type Runtime struct {
	// KeyStore is the registered keystore, nil when no keystore module is registered.
	KeyStore KeyStore
	// Active is the messaging.seal.active selector: Logical kid -> "v<N>".
	Active map[string]string
	// Tenancy is the deployment's messaging tenancy as the opener must judge tid.
	Tenancy Tenancy
	// Meter feeds the seal metrics; nil means no-op instruments.
	Meter metric.MeterProvider
}

// Spec is the codec's scanned declaration of one seal-tagged type, opaque here.
type Spec interface {
	SignLogical() string
	EncryptLogical() string
}

// Sealer turns one event of a declared type into its sealed wire bytes.
type Sealer interface {
	Seal(ctx context.Context, evt any) ([]byte, error)
}

// TenantRule is the tid expectation for one delivery, derived by the consumer door from
// Tenancy and the carrier: Required makes an absent signed tid poison; a non-empty Expected
// must equal a present tid. Implemented (derived) by messaging/sealed in #1359.
type TenantRule struct {
	Required bool
	Expected string
}

// Envelope is the verified signed envelope of one opened message — field-for-field the
// shape messaging exposes on Metadata, so the mapping is a struct conversion. Produced by
// messaging/sealed in #1359.
type Envelope struct {
	JTI        string
	IssuedAt   time.Time
	EventType  string
	TenantID   string
	SignKid    string
	SignFamily string
	EncKid     string
}

// OpenRefusedError is a sealed delivery the opener refused: Code is the rule that fired,
// Details carries presence/length facts only (never a wire value), Recoverable marks the
// provisioning-recoverable unknown-generation case, and Cause keeps the codec's own error
// for errors.As downstream. Returned by messaging/sealed in #1359.
type OpenRefusedError struct {
	Code        string
	Details     map[string]string
	Recoverable bool
	Cause       error
}

// Error renders the code and details only.
func (e *OpenRefusedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if len(e.Details) == 0 {
		return "sealed open refused: " + e.Code
	}
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("sealed open refused: ")
	b.WriteString(e.Code)
	b.WriteString(" (")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(e.Details[k])
	}
	b.WriteString(")")
	return b.String()
}

// Unwrap exposes the codec's error for errors.As / errors.Is.
func (e *OpenRefusedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Opener turns one sealed delivery body back into the event (into out) and its verified
// Envelope, judging tid by want. Implemented by messaging/sealed in #1359.
type Opener interface {
	Open(ctx context.Context, body []byte, want TenantRule, out any) (Envelope, error)
}

// OpenerFactory is the OPTIONAL consume side of a registered Codec: the consumer door
// type-asserts Registered() to it. NewOpener is the consumer's startup fail-fast (families
// provisioned in the inherited roles) bound to the declaration's EventType. Implemented by
// messaging/sealed in #1359.
type OpenerFactory interface {
	NewOpener(spec Spec, eventType string, rt *Runtime) (Opener, error)
}

// Codec is what messaging/sealed registers. ScanType returns (nil, nil) for a type that
// carries no seal tags; NewSealer is the producer's startup fail-fast (Activation, roles)
// and returns a Sealer bound to the declaration's EventType.
type Codec interface {
	ScanType(t reflect.Type) (Spec, error)
	NewSealer(spec Spec, eventType string, rt *Runtime) (Sealer, error)
}

var (
	mu      sync.RWMutex
	codec   Codec
	runtime *Runtime
	metrics *Metrics
)

// Register installs the codec. messaging/sealed calls it from init; a second call panics.
func Register(c Codec) {
	if c == nil {
		panic("sealruntime: Register called with nil")
	}
	mu.Lock()
	defer mu.Unlock()
	if codec != nil {
		panic("sealruntime: sealing codec already registered")
	}
	codec = c
}

// Registered returns the installed codec, or nil when messaging/sealed is not linked.
func Registered() Codec {
	mu.RLock()
	defer mu.RUnlock()
	return codec
}

// Configure records the runtime facts. The app is the single writer and calls it before
// collecting declarations; a later call replaces the facts (the app rebuilds them per
// process start, tests per case), and the seal instruments are rebuilt from the meter.
func Configure(rt *Runtime) {
	if rt == nil {
		panic("sealruntime: Configure called with nil")
	}
	rtCopy := *rt
	mu.Lock()
	defer mu.Unlock()
	runtime = &rtCopy
	metrics = newMetrics(rtCopy.Meter)
}

// Configured returns the runtime facts, or nil before Configure ran.
func Configured() *Runtime {
	mu.RLock()
	defer mu.RUnlock()
	return runtime
}

// Reset clears codec, runtime and metrics. Tests only.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	codec, runtime, metrics = nil, nil, nil
}

// Metric names and attribute keys shared by the seal and open paths.
const (
	MeterName               = "github.com/gaborage/go-bricks/messaging/sealed"
	MetricOperationDuration = "seal.operation.duration"
	MetricOpenFailures      = "seal.open.failures.total"
	AttrOperation           = "seal.operation"
	AttrCode                = "seal.error.code"
	OpSeal                  = "seal"
	OpOpen                  = "open"
)

// Metrics holds the seal instruments; nil-safe so an unconfigured process records nothing.
type Metrics struct {
	duration     metric.Float64Histogram
	openFailures metric.Int64Counter
}

func newMetrics(mp metric.MeterProvider) *Metrics {
	if mp == nil {
		mp = metricnoop.NewMeterProvider()
	}
	meter := mp.Meter(MeterName)
	duration, err := meter.Float64Histogram(MetricOperationDuration,
		metric.WithDescription("Duration of a payload seal or open"),
		metric.WithUnit("s"))
	if err != nil {
		duration, _ = metricnoop.NewMeterProvider().Meter(MeterName).Float64Histogram(MetricOperationDuration)
	}
	failures, err := meter.Int64Counter(MetricOpenFailures,
		metric.WithDescription("Sealed messages refused by the opener, by code"))
	if err != nil {
		failures, _ = metricnoop.NewMeterProvider().Meter(MeterName).Int64Counter(MetricOpenFailures)
	}
	return &Metrics{duration: duration, openFailures: failures}
}

// Instruments returns the configured instruments, or a no-op set before Configure ran.
func Instruments() *Metrics {
	mu.RLock()
	m := metrics
	mu.RUnlock()
	if m == nil {
		return newMetrics(nil)
	}
	return m
}

// RecordOperation records one seal or open duration.
func (m *Metrics) RecordOperation(ctx context.Context, op string, d time.Duration) {
	if m == nil {
		return
	}
	m.duration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String(AttrOperation, op)))
}

// RecordOpenFailure counts one refused open by code (codes only — never a wire value).
func (m *Metrics) RecordOpenFailure(ctx context.Context, code string) {
	if m == nil {
		return
	}
	m.openFailures.Add(ctx, 1, metric.WithAttributes(attribute.String(AttrCode, code)))
}
