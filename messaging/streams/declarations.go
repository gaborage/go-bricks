package streams

import (
	"errors"
	"fmt"
)

// streamDeclaration is one declared stream and its retention spec.
type streamDeclaration struct {
	Name string
	Spec StreamSpec
}

// consumerDeclaration is one declared consumer, deep-copied from the caller's
// ConsumerOptions at registration time.
type consumerDeclaration struct {
	Stream  string
	Name    string
	Start   OffsetStart
	SAC     bool
	Handler Handler
}

// consumerKey identifies a consumer for duplicate detection.
type consumerKey struct {
	Stream string
	Name   string
}

// Stats summarizes a declaration store.
type Stats struct {
	Streams   int
	Consumers int
}

// Declarations collects the stream infrastructure and consumers a set of modules
// declares. It is populated once at startup, validated, then replayed by Manager.
type Declarations struct {
	streams       []*streamDeclaration
	streamIndex   map[string]*streamDeclaration
	consumers     []*consumerDeclaration
	consumerIndex map[consumerKey]*consumerDeclaration
	conflicts     []error
}

// NewDeclarations creates an empty declaration store.
func NewDeclarations() *Declarations {
	return &Declarations{
		streamIndex:   make(map[string]*streamDeclaration),
		consumerIndex: make(map[consumerKey]*consumerDeclaration),
	}
}

// DeclareStream registers a stream. A nil spec leaves retention to the broker.
// Re-declaring the same name with an identical spec is a no-op; a conflicting
// spec is reported by Validate.
func (d *Declarations) DeclareStream(name string, spec *StreamSpec) {
	decl := &streamDeclaration{Name: name}
	if spec != nil {
		decl.Spec = *spec
	}

	if existing, ok := d.streamIndex[name]; ok {
		if existing.Spec != decl.Spec {
			d.conflicts = append(d.conflicts, fmt.Errorf(
				"stream %q declared twice with different retention specs (%+v vs %+v)",
				name, existing.Spec, decl.Spec))
		}
		return
	}

	d.streamIndex[name] = decl
	d.streams = append(d.streams, decl)
}

// DeclareConsumer registers a stream consumer.
// Panics if the same (stream, name) pair was already declared — a duplicate is a
// programming error that would otherwise start two members of the same offset
// group inside one process.
func (d *Declarations) DeclareConsumer(opts *ConsumerOptions) {
	if opts == nil {
		return
	}

	key := consumerKey{Stream: opts.Stream, Name: opts.Name}
	if _, exists := d.consumerIndex[key]; exists {
		panic(fmt.Sprintf(
			"streams: duplicate consumer declaration detected\n"+
				"  stream=%s name=%s\n"+
				"  Ensure each DeclareConsumer call is unique within DeclareStreams",
			opts.Stream, opts.Name,
		))
	}

	decl := &consumerDeclaration{
		Stream:  opts.Stream,
		Name:    opts.Name,
		Start:   opts.Start,
		SAC:     opts.SAC,
		Handler: opts.Handler,
	}
	d.consumerIndex[key] = decl
	d.consumers = append(d.consumers, decl)
}

// Validate reports every problem in the store at once.
func (d *Declarations) Validate() error {
	errs := append([]error(nil), d.conflicts...)

	for _, s := range d.streams {
		if s.Name == "" {
			errs = append(errs, errors.New("stream declaration has an empty name"))
		}
	}

	for _, c := range d.consumers {
		if c.Name == "" {
			errs = append(errs, fmt.Errorf("consumer on stream %q has an empty name; a name is required for offset tracking", c.Stream))
		}
		if c.Handler == nil {
			errs = append(errs, fmt.Errorf("consumer %q on stream %q has a nil handler", c.Name, c.Stream))
		}
		if _, ok := d.streamIndex[c.Stream]; !ok {
			errs = append(errs, fmt.Errorf("consumer %q references undeclared stream %q", c.Name, c.Stream))
		}
	}

	return errors.Join(errs...)
}

// IsEmpty reports whether nothing was declared.
func (d *Declarations) IsEmpty() bool {
	return len(d.streams) == 0 && len(d.consumers) == 0
}

// Stats returns the declaration counts.
func (d *Declarations) Stats() Stats {
	return Stats{Streams: len(d.streams), Consumers: len(d.consumers)}
}
