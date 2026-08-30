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
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
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
	jobID     string
	trigger   string
	log       logger.Logger
	db        dbtypes.Interface
	msgClient messaging.Client
	cfg       *config.Config
}

// fakeDBKey is the context key under which newFakeJobCtx stashes the test db, so getDB
// closures can recover it via a context VALUE rather than type-asserting the context back
// to a JobContext. Reading a value survives the context wrapping that SetTenant and the
// per-tenant lease scope apply — matching how production deps.DB resolves the tenant via
// multitenant.GetTenant rather than relying on the concrete context type.
type fakeDBKey struct{}

func newFakeJobCtx(db dbtypes.Interface, msgClient messaging.Client) *fakeJobCtx {
	return &fakeJobCtx{
		Context:   context.WithValue(context.Background(), fakeDBKey{}, db),
		jobID:     "outbox-test-job",
		trigger:   "scheduled",
		log:       logger.New("disabled", true),
		db:        db,
		msgClient: msgClient,
	}
}

// dbFromCtx recovers the db stashed by newFakeJobCtx from a context value. Returns nil when
// no db was supplied (the "database not available" test cases).
func dbFromCtx(ctx context.Context) dbtypes.Interface {
	db, _ := ctx.Value(fakeDBKey{}).(dbtypes.Interface)
	return db
}

func (c *fakeJobCtx) JobID() string               { return c.jobID }
func (c *fakeJobCtx) TriggerType() string         { return c.trigger }
func (c *fakeJobCtx) Logger() logger.Logger       { return c.log }
func (c *fakeJobCtx) DB() dbtypes.Interface       { return c.db }
func (c *fakeJobCtx) Messaging() messaging.Client { return c.msgClient }
func (c *fakeJobCtx) Config() *config.Config      { return c.cfg }

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

	// Call counters and last-arg captures.
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

// fakeAMQP is a minimal messaging.AMQPClient implementation for relay
// tests. The relay only uses IsReady and PublishToExchange; the other
// AMQPClient methods are present to satisfy the interface and return zero
// values when invoked.
type fakeAMQP struct {
	mu sync.Mutex

	Ready bool
	// Configurable returns for PublishToExchange. PublishErrFor matches by
	// exchange + routing key — first hit wins. PublishErr is the fallback.
	PublishErrFor map[string]error
	PublishErr    error
	// PublishBlock, keyed by exchange:routingKey, makes PublishToExchange block
	// until ctx is done and then return ctx.Err() — simulating a stuck broker that
	// the relay's per-record PublishTimeout must interrupt without starving siblings.
	PublishBlock map[string]bool
	// PublishHook, if set, runs synchronously inside PublishToExchange (while
	// still holding the internal lock) right after PublishCalls is incremented
	// and before the configured error is resolved. Lets a test flip Ready
	// exactly on a specific call number to simulate the broker dropping
	// connectivity mid-batch (rather than being down for the whole cycle).
	PublishHook func(f *fakeAMQP)

	// Captured calls.
	PublishCalls    int
	LastPublishOpts messaging.PublishOptions
	LastPublishData []byte
	LastPublishHdrs map[string]any
	LastPublishCtx  context.Context
}

func newFakeAMQP() *fakeAMQP {
	return &fakeAMQP{Ready: true}
}

func (f *fakeAMQP) IsReady() bool { return f.Ready }

func (f *fakeAMQP) Publish(_ context.Context, _ string, _ []byte) error { return nil }

func (f *fakeAMQP) PublishToExchange(ctx context.Context, opts messaging.PublishOptions, data []byte) error {
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
	block := f.PublishBlock[key]
	err := f.PublishErr
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
	mu    *sync.Mutex
	lines *[]string
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{mu: &sync.Mutex{}, lines: &[]string{}}
}

func (l *recordingLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), *l.lines...)
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

func (e *recordingEvent) Msgf(format string, args ...any)           { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recordingEvent) Err(error) logger.LogEvent                 { return e }
func (e *recordingEvent) Str(_, _ string) logger.LogEvent           { return e }
func (e *recordingEvent) Int(string, int) logger.LogEvent           { return e }
func (e *recordingEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e *recordingEvent) Uint64(string, uint64) logger.LogEvent     { return e }
func (e *recordingEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *recordingEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *recordingEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e *recordingEvent) Bool(string, bool) logger.LogEvent         { return e }
func (e *recordingEvent) Enabled() bool                             { return true }
