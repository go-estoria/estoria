package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

// A StreamProjection reads events from an event stream and executes a projection function for each event.
type StreamProjection struct {
	events   eventstore.StreamReader
	streamID typeid.UUID
	readOps  eventstore.ReadStreamOptions

	log estoria.Logger
}

// New creates a new StreamProjection.
func New(events eventstore.StreamReader, streamID typeid.UUID, opts ...StreamProjectionOption) (*StreamProjection, error) {
	switch {
	case events == nil:
		return nil, errors.New("event stream reader is required")
	case streamID.IsEmpty():
		return nil, errors.New("stream ID is required")
	}

	projection := &StreamProjection{
		events:   events,
		streamID: streamID,
		readOps:  eventstore.ReadStreamOptions{},
		log:      estoria.GetLogger().WithGroup("projection"),
	}

	for _, opt := range opts {
		opt(projection)
	}

	return projection, nil
}

// An EventHandler handles an individual event.
type EventHandler interface {
	Handle(ctx context.Context, event *eventstore.Event) error
}

// An EventHandlerFunc is a function that handles an event during projection.
type EventHandlerFunc func(ctx context.Context, event *eventstore.Event) error

// Handle implements the EventHandler interface, allowing an EventHandlerFunc to be used as an EventHandler.
func (f EventHandlerFunc) Handle(ctx context.Context, event *eventstore.Event) error {
	return f(ctx, event)
}

// Result contains the result of a projection.
type Result struct {
	NumProjectedEvents int64
}

func (p *StreamProjection) Project(ctx context.Context, eventHandler EventHandler) (*Result, error) {
	iter, err := p.events.ReadStream(ctx, p.streamID, p.readOps)
	if err != nil {
		return nil, fmt.Errorf("obtaining stream iterator: %w", err)
	}

	defer iter.Close(ctx)

	result := &Result{}

	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			break
		} else if err != nil {
			return result, fmt.Errorf("reading event: %w", err)
		}

		p.log.Debug("projecting event", "stream_id", p.streamID, "event_id", event.ID, "stream_version", event.StreamVersion)

		if err := eventHandler.Handle(ctx, event); err != nil {
			return result, fmt.Errorf("processing event: %w", err)
		}

		result.NumProjectedEvents++
	}

	p.log.Debug("projected events", "stream_id", p.streamID, "count", result.NumProjectedEvents)

	return result, nil
}

// A StreamProjectionOption is an option for configuring a StreamProjection.
type StreamProjectionOption func(*StreamProjection)

// WithReadStreamOptions sets the options used for reading the event stream.
func WithReadStreamOptions(opts eventstore.ReadStreamOptions) StreamProjectionOption {
	return func(p *StreamProjection) {
		p.readOps = opts
	}
}

// WithLogger sets the logger for the StreamProjection.
func WithLogger(log estoria.Logger) StreamProjectionOption {
	return func(p *StreamProjection) {
		p.log = log
	}
}
