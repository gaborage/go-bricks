package outbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/publishdoor"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
)

// oversizedShortStr is one byte past what an AMQP shortstr can carry, and
// maxLengthShortStr is exactly at the ceiling — the pair every destination-bound
// test needs, since a guard that only rejects proves nothing about where the
// boundary sits.
var (
	oversizedShortStr = strings.Repeat("k", 256)
	maxLengthShortStr = strings.Repeat("k", 255)
)

// fakeJobCtx is a minimal scheduler.JobContext implementation for outbox
// relay/cleanup unit tests. The embedded context.Context satisfies the
// stdlib half of the interface; the other accessors are field-backed.
type fakeJobCtx struct {
	context.Context
	jobID   string
	trigger string
	log     logger.Logger
	db      dbtypes.Interface
	cfg     *config.Config
}

// fakeDBKey is the context key under which newFakeJobCtx stashes the test db, so getDB
// closures can recover it via a context VALUE rather than type-asserting the context back
// to a JobContext. Reading a value survives the context wrapping that SetTenant and the
// per-tenant lease scope apply — matching how production deps.DB resolves the tenant via
// multitenant.GetTenant rather than relying on the concrete context type.
type fakeDBKey struct{}

func newFakeJobCtx(db dbtypes.Interface) *fakeJobCtx {
	return &fakeJobCtx{
		Context: context.WithValue(context.Background(), fakeDBKey{}, db),
		jobID:   "outbox-test-job",
		trigger: "scheduled",
		log:     logger.New("disabled", true),
		db:      db,
	}
}

// dbFromCtx recovers the db stashed by newFakeJobCtx from a context value. Returns nil when
// no db was supplied (the "database not available" test cases).
func dbFromCtx(ctx context.Context) dbtypes.Interface {
	db, _ := ctx.Value(fakeDBKey{}).(dbtypes.Interface)
	return db
}

func (c *fakeJobCtx) JobID() string         { return c.jobID }
func (c *fakeJobCtx) TriggerType() string   { return c.trigger }
func (c *fakeJobCtx) Logger() logger.Logger { return c.log }
func (c *fakeJobCtx) DB() dbtypes.Interface { return c.db }

// Messaging is nil for every relay test: since ADR-088 each lane's adapter resolves its
// own client, so nothing the relay does reads the JobContext's.
func (c *fakeJobCtx) Messaging() messaging.AMQPClient { return nil }
func (c *fakeJobCtx) Config() *config.Config          { return c.cfg }

// fakeStore implements the outbox Store interface with configurable
// return values and call-count tracking. Methods are concurrency-safe via
// a single mutex so tests can assert on call counts without races.
type fakeStore struct {
	mu sync.Mutex

	// Configurable returns.
	InsertErr           error
	FetchPendingResult  []Record
	FetchPendingErr     error
	MarkPublishedErr    error
	MarkFailedErr       error
	MarkDeadLetteredErr error
	DeletePublishedN    int64
	DeletePublishedErr  error
	CreateTableErr      error
	LeadErr             error
	ProbeErr            error
	ProbeErrAfter       int // Probe returns ProbeErr from this call number on; 0 disables

	// Call counters and last-arg captures.
	LeadCalls               int
	ProbeCalls              int
	ReleaseCalls            int
	InsertCalls             int
	FetchPendingCalls       int
	FetchPendingLastBatch   int
	MarkPublishedCalls      int
	MarkPublishedLastID     string
	MarkFailedCalls         int
	MarkFailedLastID        string
	MarkFailedLastErr       string
	MarkDeadLetteredCalls   int
	MarkDeadLetteredLastID  string
	MarkDeadLetteredLastErr string
	DeletePublishedCalls    int
	DeletePublishedCutoff   time.Time
	CreateTableCalls        int
}

// Lead returns a leadership bound to the fake's counters. The zero value leads and
// probes clean, so a test that does not care about leadership needs no setup.
func (s *fakeStore) Lead(_ context.Context, _ dbtypes.Interface) (Leadership, error) {
	s.mu.Lock()
	s.LeadCalls++
	err := s.LeadErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &fakeLeadership{store: s}, nil
}

type fakeLeadership struct {
	store *fakeStore
}

func (l *fakeLeadership) Probe(context.Context) error {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	l.store.ProbeCalls++
	if l.store.ProbeErrAfter > 0 && l.store.ProbeCalls >= l.store.ProbeErrAfter {
		return l.store.ProbeErr
	}
	return nil
}

func (l *fakeLeadership) Release(context.Context) error {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	l.store.ReleaseCalls++
	return nil
}

func (s *fakeStore) Insert(_ context.Context, _ dbtypes.Tx, _ *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.InsertCalls++
	return s.InsertErr
}

func (s *fakeStore) FetchPending(_ context.Context, _ dbtypes.Interface, batchSize int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FetchPendingCalls++
	s.FetchPendingLastBatch = batchSize
	return s.FetchPendingResult, s.FetchPendingErr
}

func (s *fakeStore) MarkPublished(_ context.Context, _ dbtypes.Interface, eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MarkPublishedCalls++
	s.MarkPublishedLastID = eventID
	return s.MarkPublishedErr
}

func (s *fakeStore) MarkFailed(_ context.Context, _ dbtypes.Interface, eventID, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MarkFailedCalls++
	s.MarkFailedLastID = eventID
	s.MarkFailedLastErr = errMsg
	return s.MarkFailedErr
}

func (s *fakeStore) MarkDeadLettered(_ context.Context, _ dbtypes.Interface, eventID, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MarkDeadLetteredCalls++
	s.MarkDeadLetteredLastID = eventID
	s.MarkDeadLetteredLastErr = errMsg
	return s.MarkDeadLetteredErr
}

func (s *fakeStore) DeletePublished(_ context.Context, _ dbtypes.Interface, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DeletePublishedCalls++
	s.DeletePublishedCutoff = before
	return s.DeletePublishedN, s.DeletePublishedErr
}

func (s *fakeStore) CreateTable(_ context.Context, _ dbtypes.Interface) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CreateTableCalls++
	return s.CreateTableErr
}

// fakeStreamPublisher stands in for a *streams.Publisher so the stream leg is testable
// without a broker.
type fakeStreamPublisher struct {
	mu sync.Mutex

	Err error
	// NotReady makes the handle report itself unusable, which is what the stream lane's
	// per-target pre-flight reads.
	NotReady bool
	// IsClosed stands in for a producer the manager already stopped, which the lane must
	// read as a shutdown rather than as this row failing.
	IsClosed bool

	Calls   int
	LastMsg *streams.PublishMessage
}

func (f *fakeStreamPublisher) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.NotReady
}

func (f *fakeStreamPublisher) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.IsClosed
}

func (f *fakeStreamPublisher) Publish(_ context.Context, msg *streams.PublishMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	f.LastMsg = msg
	return f.Err
}

// fakeAMQP is a minimal messaging.AMQPClient implementation for the AMQP shipper's
// tests. The shipper only uses IsReady and the byte door; the other AMQPClient methods
// are present to satisfy the interface and return zero values when invoked. The byte
// door is unexported inside messaging (ADR-096), so newAMQPShipperWithFake hands the
// adapter's publish field a func routing to publishBytes below rather than swapping the
// process-wide dispatcher.
type fakeAMQP struct {
	mu sync.Mutex

	Ready bool
	// Configurable returns for publishBytes. PublishErrFor matches by
	// exchange + routing key — first hit wins. PublishErr is the fallback.
	PublishErrFor map[string]error
	PublishErr    error
	// PublishBlock, keyed by exchange:routingKey, makes publishBytes block
	// until ctx is done and then return ctx.Err() — simulating a stuck broker that
	// the relay's per-record PublishTimeout must interrupt without starving siblings.
	PublishBlock map[string]bool
	// PublishHook, if set, runs synchronously inside publishBytes (while
	// still holding the internal lock) right after PublishCalls is incremented
	// and before the configured error is resolved. Lets a test flip Ready
	// exactly on a specific call number to simulate the broker dropping
	// connectivity mid-batch (rather than being down for the whole cycle).
	PublishHook func(f *fakeAMQP)

	// PublishErrOnce, keyed by exchange:routingKey, fails only the FIRST publish for
	// that key — so a later cycle (or a later row of a different key) succeeds.
	PublishErrOnce map[string]error

	// Captured calls.
	PublishOrder    []string
	PublishCalls    int
	LastPublishOpts publishdoor.Options
	LastPublishData []byte
	LastPublishHdrs map[string]any
	LastPublishCtx  context.Context
}

func newFakeAMQP() *fakeAMQP {
	return &fakeAMQP{Ready: true}
}

func (f *fakeAMQP) IsReady() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Ready
}

// newAMQPShipperWithFake wires the production AMQP adapter to a fakeAMQP.
func newAMQPShipperWithFake(f *fakeAMQP) *amqpShipper {
	return &amqpShipper{
		client: func(context.Context) (messaging.AMQPClient, error) { return f, nil },
		publish: func(ctx context.Context, _ messaging.AMQPClient, opts publishdoor.Options, data []byte) error {
			return f.publishBytes(ctx, opts, data)
		},
	}
}

func (f *fakeAMQP) publishBytes(ctx context.Context, opts publishdoor.Options, data []byte) error {
	f.mu.Lock()
	f.PublishCalls++
	f.LastPublishOpts = opts
	f.LastPublishData = data
	f.LastPublishHdrs = opts.Headers
	f.LastPublishCtx = ctx
	if f.PublishHook != nil {
		f.PublishHook(f)
	}
	key := opts.Exchange + ":" + opts.RoutingKey
	f.PublishOrder = append(f.PublishOrder, opts.RoutingKey)
	block := f.PublishBlock[key]
	err := f.PublishErr
	if e, found := f.PublishErrOnce[key]; found {
		delete(f.PublishErrOnce, key)
		err = e
	}
	if e, found := f.PublishErrFor[key]; found {
		err = e
	}
	f.mu.Unlock()

	// Block (lock released) until the per-record context is canceled/expired,
	// then surface its error — exercising the relay's per-record timeout.
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeAMQP) Consume(_ context.Context, _ string) (<-chan amqp.Delivery, error) {
	return nil, nil
}

func (f *fakeAMQP) ConsumeFromQueue(_ context.Context, _ messaging.ConsumeOptions) (<-chan amqp.Delivery, error) {
	return nil, nil
}

func (f *fakeAMQP) DeclareQueue(_ context.Context, _ *messaging.QueueDeclaration) error { return nil }

func (f *fakeAMQP) DeclareExchange(_ context.Context, _ *messaging.ExchangeDeclaration) error {
	return nil
}

func (f *fakeAMQP) BindQueue(_ context.Context, _ *messaging.BindingDeclaration) error { return nil }

func (f *fakeAMQP) Close() error { return nil }

// recordingLogger captures the message text of emitted lines. The relay's
// secondary-error paths — "could not write down why this record failed" — have
// no return value and no store side effect, so the emitted line is the only
// observable they have.
type recordingLogger struct {
	mu     *sync.Mutex
	lines  *[]string
	fields *map[string]int64
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{mu: &sync.Mutex{}, lines: &[]string{}, fields: &map[string]int64{}}
}

func (l *recordingLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), *l.lines...)
}

// numbers returns the numeric fields the recorded lines carried. logCycle's counts have
// no other observable: Execute discards the result it summarizes.
func (l *recordingLogger) numbers() map[string]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int64, len(*l.fields))
	for k, v := range *l.fields {
		out[k] = v
	}
	return out
}

func (l *recordingLogger) event() logger.LogEvent                  { return &recordingEvent{owner: l} }
func (l *recordingLogger) Info() logger.LogEvent                   { return l.event() }
func (l *recordingLogger) Error() logger.LogEvent                  { return l.event() }
func (l *recordingLogger) Debug() logger.LogEvent                  { return l.event() }
func (l *recordingLogger) Warn() logger.LogEvent                   { return l.event() }
func (l *recordingLogger) Fatal() logger.LogEvent                  { return l.event() }
func (l *recordingLogger) WithContext(any) logger.Logger           { return l }
func (l *recordingLogger) WithFields(map[string]any) logger.Logger { return l }

type recordingEvent struct{ owner *recordingLogger }

func (e *recordingEvent) Msg(msg string) {
	e.owner.mu.Lock()
	defer e.owner.mu.Unlock()
	*e.owner.lines = append(*e.owner.lines, msg)
}

func (e *recordingEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recordingEvent) Err(error) logger.LogEvent       { return e }
func (e *recordingEvent) Str(_, _ string) logger.LogEvent { return e }
func (e *recordingEvent) Int(key string, value int) logger.LogEvent {
	return e.Int64(key, int64(value))
}

func (e *recordingEvent) Int64(key string, value int64) logger.LogEvent {
	e.owner.mu.Lock()
	defer e.owner.mu.Unlock()
	(*e.owner.fields)[key] = value
	return e
}
func (e *recordingEvent) Uint64(string, uint64) logger.LogEvent     { return e }
func (e *recordingEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *recordingEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *recordingEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e *recordingEvent) Bool(string, bool) logger.LogEvent         { return e }
func (e *recordingEvent) Enabled() bool                             { return true }

// fakeShipper stands in for one lane's adapter, so the relay's own work — leadership,
// the ledger writes, parking, down-scopes, the publish bound and the cycle log — is
// exercised without either broker (ADR-088). Verdicts are consumed in the order the lane
// is handed rows, and an exhausted list delivers.
type fakeShipper struct {
	ReadyErr error
	// ReadyFn, when set, replaces ReadyErr and is called with the context the relay's
	// preflight actually hands Ready — lets a test observe whether that context carries a
	// deadline (#1538) rather than only what error it returns.
	ReadyFn  func(ctx context.Context) error
	Plans    func(rec *Record, headers map[string]any) shipment
	Verdicts []verdict

	ReadyCalls int
	Ships      []shipment
	Ctxs       []context.Context
}

func (f *fakeShipper) Ready(ctx context.Context) error {
	f.ReadyCalls++
	if f.ReadyFn != nil {
		return f.ReadyFn(ctx)
	}
	return f.ReadyErr
}

func (f *fakeShipper) Plan(rec *Record, headers map[string]any) shipment {
	if f.Plans != nil {
		return f.Plans(rec, headers)
	}
	return shipment{Record: rec, Headers: headers, Key: rec.Exchange + ":" + rec.RoutingKey}
}

func (f *fakeShipper) Ship(ctx context.Context, s *shipment) verdict {
	// Copied out: the relay reuses ONE shipment per cycle, so keeping the pointer would
	// make every recorded ship read as the last one.
	f.Ships = append(f.Ships, *s)
	f.Ctxs = append(f.Ctxs, ctx)
	if len(f.Verdicts) == 0 {
		return verdict{Kind: shipDelivered}
	}
	v := f.Verdicts[0]
	f.Verdicts = f.Verdicts[1:]
	return v
}

// shippedIDs is the order the lane was actually handed rows in.
func (f *fakeShipper) shippedIDs() []string {
	ids := make([]string, 0, len(f.Ships))
	for i := range f.Ships {
		ids = append(ids, f.Ships[i].Record.ID)
	}
	return ids
}

// streamRow mirrors what applyStreamTarget actually persists: the tenant lives in
// partition_key and NOT in the headers. An earlier version of this fixture hand-wrote an
// x-tenant-id header the writer never produces, which hid that a real stream row reached
// the publisher unstamped under shared tenancy.
func streamRow() Record {
	return Record{
		ID: "S1", Lane: LaneStream, Stream: "customers", PartitionKey: "acme",
		EventType: "customer.created", Payload: []byte("p"),
		Headers: []byte(`{"traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}`),
	}
}

// streamShipperWith wires the production stream adapter to a single fake handle under
// the name streamRow() targets.
func streamShipperWith(pub streamPublisher) *streamShipper {
	return newStreamShipper(func(name string) (streamPublisher, bool) {
		if name == "customers" && pub != nil {
			return pub, true
		}
		return nil, false
	})
}
