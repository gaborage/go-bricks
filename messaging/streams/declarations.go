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

// superStreamDeclaration is one declared super stream: a stream partitioned into
// Partitions individual streams the broker names "<Name>-0" … "<Name>-(n-1)".
// The spec applies to every partition.
type superStreamDeclaration struct {
	Name       string
	Partitions int
	Spec       StreamSpec
}

// consumerDeclaration is one declared consumer, deep-copied from the caller's
// options at registration time. Super marks which of the two declare methods
// produced it, because the two consume through different client APIs.
type consumerDeclaration struct {
	Stream  string
	Name    string
	Start   OffsetStart
	SAC     bool
	Handler Handler
	Super   bool
}

// consumerKey identifies a consumer for duplicate detection.
type consumerKey struct {
	Stream string
	Name   string
}

// Stats summarizes a declaration store.
type Stats struct {
	// Streams counts every declared stream, super streams included.
	Streams int
	// SuperStreams counts how many of Streams are partitioned super streams.
	SuperStreams int
	// Consumers counts every declared consumer, of either kind.
	Consumers int
}

// Declarations collects the stream infrastructure and consumers a set of modules
// declares. It is populated once at startup, validated, then replayed by Manager.
type Declarations struct {
	streams          []*streamDeclaration
	streamIndex      map[string]*streamDeclaration
	superStreams     []*superStreamDeclaration
	superStreamIndex map[string]*superStreamDeclaration
	consumers        []*consumerDeclaration
	consumerIndex    map[consumerKey]*consumerDeclaration
	conflicts        []error
}

// NewDeclarations creates an empty declaration store.
func NewDeclarations() *Declarations {
	return &Declarations{
		streamIndex:      make(map[string]*streamDeclaration),
		superStreamIndex: make(map[string]*superStreamDeclaration),
		consumerIndex:    make(map[consumerKey]*consumerDeclaration),
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

// DeclareSuperStream registers a super stream of partitions partitions. A nil
// spec leaves retention to the broker; a non-nil one applies to every partition.
// Re-declaring the same name identically is a no-op; a conflicting partition
// count or spec is reported by Validate.
func (d *Declarations) DeclareSuperStream(name string, partitions int, spec *StreamSpec) {
	decl := &superStreamDeclaration{Name: name, Partitions: partitions}
	if spec != nil {
		decl.Spec = *spec
	}

	if existing, ok := d.superStreamIndex[name]; ok {
		if *existing != *decl {
			d.conflicts = append(d.conflicts, fmt.Errorf(
				"super stream %q declared twice with different definitions (%+v vs %+v)",
				name, *existing, *decl))
		}
		return
	}

	d.superStreamIndex[name] = decl
	d.superStreams = append(d.superStreams, decl)
}

// DeclareConsumer registers a stream consumer.
// Panics on a nil options pointer, and if the same (stream, name) pair was
// already declared. Both are programming errors: a nil declaration would consume
// nothing at all, and a duplicate would otherwise start two members of the same
// offset group inside one process.
func (d *Declarations) DeclareConsumer(opts *ConsumerOptions) {
	if opts == nil {
		panic(nilConsumerPanic("DeclareConsumer", "ConsumerOptions"))
	}

	d.registerConsumer(&consumerDeclaration{
		Stream:  opts.Stream,
		Name:    opts.Name,
		Start:   opts.Start,
		SAC:     opts.SAC,
		Handler: opts.Handler,
	})
}

// DeclareSuperStreamConsumer registers one consumer over every partition of a
// super stream. Panics on the same two programming errors as DeclareConsumer.
//
// The declaration is always a single active consumer group — see
// SuperStreamConsumerOptions for why the choice is not the caller's.
func (d *Declarations) DeclareSuperStreamConsumer(opts *SuperStreamConsumerOptions) {
	if opts == nil {
		panic(nilConsumerPanic("DeclareSuperStreamConsumer", "SuperStreamConsumerOptions"))
	}

	d.registerConsumer(&consumerDeclaration{
		Stream:  opts.SuperStream,
		Name:    opts.Name,
		Start:   opts.Start,
		SAC:     true,
		Handler: opts.Handler,
		Super:   true,
	})
}

// registerConsumer stores a consumer declaration of either kind, rejecting a
// duplicate (target, name) pair in the vocabulary of the method that produced it.
func (d *Declarations) registerConsumer(decl *consumerDeclaration) {
	method, label := "DeclareConsumer", "stream"
	if decl.Super {
		method, label = "DeclareSuperStreamConsumer", "super stream"
	}

	key := consumerKey{Stream: decl.Stream, Name: decl.Name}
	if _, exists := d.consumerIndex[key]; exists {
		panic(fmt.Sprintf(
			"streams: duplicate consumer declaration detected\n"+
				"  %s=%s name=%s\n"+
				"  Ensure each %s call is unique within DeclareStreams",
			label, decl.Stream, decl.Name, method,
		))
	}

	d.consumerIndex[key] = decl
	d.consumers = append(d.consumers, decl)
}

// nilConsumerPanic renders the nil-declaration panic for one declare method.
func nilConsumerPanic(method, optionsType string) string {
	return fmt.Sprintf("streams: nil consumer declaration detected\n"+
		"  %s requires a non-nil *%s\n"+
		"  Pass &streams.%s{...} at every %s call within DeclareStreams",
		method, optionsType, optionsType, method)
}

// Validate reports every problem in the store at once.
func (d *Declarations) Validate() error {
	errs := append([]error(nil), d.conflicts...)

	for _, s := range d.streams {
		if s.Name == "" {
			errs = append(errs, errors.New("stream declaration has an empty name"))
		}
		errs = append(errs, negativeRetentionErrors("stream", s.Name, s.Spec)...)
	}

	for _, s := range d.superStreams {
		if s.Name == "" {
			errs = append(errs, errors.New("super stream declaration has an empty name"))
		}
		if s.Partitions < 1 {
			errs = append(errs, fmt.Errorf("super stream %q declares %d partitions; at least 1 is required", s.Name, s.Partitions))
		}
		if _, ok := d.streamIndex[s.Name]; ok {
			errs = append(errs, fmt.Errorf("%q is declared both as a stream and as a super stream", s.Name))
		}
		errs = append(errs, negativeRetentionErrors("super stream", s.Name, s.Spec)...)
	}

	for _, c := range d.consumers {
		if c.Name == "" {
			errs = append(errs, fmt.Errorf("consumer on stream %q has an empty name; a name is required for offset tracking", c.Stream))
		}
		if c.Handler == nil {
			errs = append(errs, fmt.Errorf("consumer %q on stream %q has a nil handler", c.Name, c.Stream))
		}
		if err := d.consumerTargetError(c); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// consumerTargetError checks that a consumer's target was declared, and declared
// as the kind the consumer consumes it as. The two kinds reach the broker through
// different client APIs, so consuming one as the other cannot work.
func (d *Declarations) consumerTargetError(c *consumerDeclaration) error {
	_, isStream := d.streamIndex[c.Stream]
	_, isSuper := d.superStreamIndex[c.Stream]

	switch {
	case c.Super && isSuper, !c.Super && isStream:
		return nil
	case c.Super && isStream:
		return fmt.Errorf("consumer %q consumes %q as a super stream, but it is declared as a plain stream; use DeclareConsumer", c.Name, c.Stream)
	case !c.Super && isSuper:
		return fmt.Errorf("consumer %q consumes %q as a plain stream, but it is declared as a super stream; use DeclareSuperStreamConsumer", c.Name, c.Stream)
	case c.Super:
		return fmt.Errorf("consumer %q references undeclared super stream %q", c.Name, c.Stream)
	default:
		return fmt.Errorf("consumer %q references undeclared stream %q", c.Name, c.Stream)
	}
}

// negativeRetentionErrors rejects negative retention values. The manager applies
// each field only when it is positive, so a negative one would be dropped on the
// way to the broker and the stream would silently retain by the broker's default
// instead of the caller's declaration. Zero is how that default is asked for.
func negativeRetentionErrors(kind, name string, spec StreamSpec) []error {
	var errs []error
	if spec.MaxAge < 0 {
		errs = append(errs, fmt.Errorf("%s %q has a negative MaxAge (%s); zero leaves retention to the broker", kind, name, spec.MaxAge))
	}
	if spec.MaxLengthBytes < 0 {
		errs = append(errs, fmt.Errorf("%s %q has a negative MaxLengthBytes (%d); zero leaves retention to the broker", kind, name, spec.MaxLengthBytes))
	}
	if spec.MaxSegmentSizeBytes < 0 {
		errs = append(errs, fmt.Errorf("%s %q has a negative MaxSegmentSizeBytes (%d); zero leaves retention to the broker", kind, name, spec.MaxSegmentSizeBytes))
	}
	return errs
}

// IsEmpty reports whether nothing was declared.
func (d *Declarations) IsEmpty() bool {
	return len(d.streams) == 0 && len(d.superStreams) == 0 && len(d.consumers) == 0
}

// Stats returns the declaration counts.
func (d *Declarations) Stats() Stats {
	return Stats{
		Streams:      len(d.streams) + len(d.superStreams),
		SuperStreams: len(d.superStreams),
		Consumers:    len(d.consumers),
	}
}
