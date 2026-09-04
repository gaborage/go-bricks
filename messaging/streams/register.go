package streams

import (
	"context"
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/internal/streamruntime"
	"github.com/gaborage/go-bricks/logger"
)

//nolint:gochecknoinits // blank import of this package is the opt-in that links the lane (ADR-091)
func init() {
	streamruntime.Register(streamRuntime{})
}

// StreamDeclarer is the optional module interface the framework detects at
// startup. Modules that implement it have DeclareStreams called automatically.
// This used to live on app.StreamDeclarer; that name was removed so app does
// not import this package (ADR-091).
type StreamDeclarer interface {
	DeclareStreams(decls *Declarations)
}

type streamRuntime struct{}

type declHandle struct {
	decls *Declarations
}

func (h declHandle) IsEmpty() bool { return h.decls.IsEmpty() }

func (h declHandle) Stats() streamruntime.DeclStats {
	s := h.decls.Stats()
	return streamruntime.DeclStats{Streams: s.Streams, Consumers: s.Consumers, Publishers: s.Publishers}
}

func (streamRuntime) CollectDeclarations(modules []streamruntime.ModuleNamer, log logger.Logger) (streamruntime.Declarations, error) {
	decls := NewDeclarations()
	for _, module := range modules {
		if sd, ok := module.(StreamDeclarer); ok {
			log.Info().
				Str("module", module.Name()).
				Msg("Collecting module stream declarations")
			sd.DeclareStreams(decls)
		}
	}

	if err := decls.Validate(); err != nil {
		log.Error().Err(err).Msg("Stream declaration validation failed")
		return nil, fmt.Errorf("stream declaration validation failed: %w", err)
	}

	stats := decls.Stats()
	log.Info().
		Int("streams", stats.Streams).
		Int("consumers", stats.Consumers).
		Msg("Stream declarations collected and validated successfully")

	return declHandle{decls: decls}, nil
}

func (streamRuntime) NewManager(opts *streamruntime.ManagerOptions) streamruntime.Handle {
	if opts == nil {
		opts = &streamruntime.ManagerOptions{}
	}
	return &managerHandle{Manager: NewManager(ManagerOptions{
		URI:                 opts.URI,
		AddressResolverHost: opts.AddressResolverHost,
		AddressResolverPort: opts.AddressResolverPort,
		OffsetStoreCount:    opts.OffsetStoreCount,
		OffsetStoreInterval: opts.OffsetStoreInterval,
		Logger:              opts.Logger,
		Hold:                opts.Hold,
	})}
}

func (streamRuntime) CanDrainHold() bool {
	_, ok := any((*Manager)(nil)).(HoldReplayer)
	return ok
}

type managerHandle struct{ *Manager }

func (h *managerHandle) Start(ctx context.Context, decls streamruntime.Declarations) error {
	d, ok := decls.(declHandle)
	if !ok {
		return errors.New("streams: declarations were not produced by this runtime")
	}
	return h.Manager.Start(ctx, d.decls)
}

var (
	_ streamruntime.Runtime      = streamRuntime{}
	_ streamruntime.Handle       = (*managerHandle)(nil)
	_ streamruntime.Declarations = declHandle{}
)
