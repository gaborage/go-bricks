package outbox

import (
	"context"
	"sync"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
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

func newFakeJobCtx(db dbtypes.Interface, msgClient messaging.Client) *fakeJobCtx {
	return &fakeJobCtx{
		Context:   context.Background(),
		jobID:     "outbox-test-job",
		trigger:   "scheduled",
		log:       logger.New("disabled", true),
		db:        db,
		msgClient: msgClient,
	}
}

func (c *fakeJobCtx) JobID() string             { return c.jobID }
func (c *fakeJobCtx) TriggerType() string       { return c.trigger }
func (c *fakeJobCtx) Logger() logger.Logger     { return c.log }
func (c *fakeJobCtx) DB() dbtypes.Interface     { return c.db }
func (c *fakeJobCtx) Messaging() messaging.Client { return c.msgClient }
func (c *fakeJobCtx) Config() *config.Config    { return c.cfg }

// fakeStore implements the outbox Store interface with configurable
// return values and call-count tracking. Methods are concurrency-safe via
// a single mutex so tests can assert on call counts without races.
type fakeStore struct {
	mu sync.Mutex

	// Configurable returns.
	InsertErr          error
	FetchPendingResult []Record
	FetchPendingErr    error
	MarkPublishedErr   error
	MarkFailedErr      error
	DeletePublishedN   int64
	DeletePublishedErr error
	CreateTableErr     error

	// Call counters and last-arg captures.
	InsertCalls           int
	FetchPendingCalls     int
	MarkPublishedCalls    int
	MarkPublishedLastID   string
	MarkFailedCalls       int
	MarkFailedLastID      string
	MarkFailedLastErr     string
	DeletePublishedCalls  int
	DeletePublishedCutoff time.Time
	CreateTableCalls      int
}

func (s *fakeStore) Insert(_ context.Context, _ dbtypes.Tx, _ *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.InsertCalls++
	return s.InsertErr
}

func (s *fakeStore) FetchPending(_ context.Context, _ dbtypes.Interface, _, _ int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FetchPendingCalls++
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

	// Captured calls.
	PublishCalls    int
	LastPublishOpts messaging.PublishOptions
	LastPublishData []byte
	LastPublishHdrs map[string]any
}

func newFakeAMQP() *fakeAMQP {
	return &fakeAMQP{Ready: true}
}

func (f *fakeAMQP) IsReady() bool { return f.Ready }

func (f *fakeAMQP) Publish(_ context.Context, _ string, _ []byte) error { return nil }

func (f *fakeAMQP) PublishToExchange(_ context.Context, opts messaging.PublishOptions, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PublishCalls++
	f.LastPublishOpts = opts
	f.LastPublishData = data
	f.LastPublishHdrs = opts.Headers
	key := opts.Exchange + ":" + opts.RoutingKey
	if err, found := f.PublishErrFor[key]; found {
		return err
	}
	return f.PublishErr
}

func (f *fakeAMQP) Consume(_ context.Context, _ string) (<-chan amqp091.Delivery, error) {
	return nil, nil
}

func (f *fakeAMQP) ConsumeFromQueue(_ context.Context, _ messaging.ConsumeOptions) (<-chan amqp091.Delivery, error) {
	return nil, nil
}

func (f *fakeAMQP) DeclareQueue(_ string, _, _, _, _ bool) error    { return nil }
func (f *fakeAMQP) DeclareExchange(_, _ string, _, _, _, _ bool) error { return nil }
func (f *fakeAMQP) BindQueue(_, _, _ string, _ bool) error          { return nil }
func (f *fakeAMQP) Close() error                                    { return nil }
