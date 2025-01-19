package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
)

// A StreamProjection reads events from an event stream and executes a projection function for each event.
type StreamProjection struct {
	iter                   eventstore.StreamIterator
	continueOnHandlerError bool

	log estoria.Logger
}

// New creates a new StreamProjection.
func New(iter eventstore.StreamIterator, opts ...StreamProjectionOption) (*StreamProjection, error) {
	if iter == nil {
		return nil, errors.New("event stream iterator is required")
	}

	projection := &StreamProjection{
		iter: iter,
		log:  estoria.GetLogger().WithGroup("projection"),
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
	// NumProjectedEvents is the number of events that were successfully projected.
	NumProjectedEvents int64

	// NumFailedEvents is the number of events that failed to project.
	NumFailedEvents int64
}

func (p *StreamProjection) Project(ctx context.Context, eventHandler EventHandler) (*Result, error) {
	if eventHandler == nil {
		return nil, errors.New("event handler is required")
	}

	result := &Result{}

	for {
		event, err := p.iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			break
		} else if err != nil {
			return result, fmt.Errorf("reading event: %w", err)
		}

		p.log.Debug("projecting event", "stream_id", event.StreamID, "event_id", event.ID, "stream_version", event.StreamVersion)

		if err := eventHandler.Handle(ctx, event); err != nil {
			result.NumFailedEvents++

			if p.continueOnHandlerError {
				p.log.Error("error handling event", "stream_id", event.StreamID, "event_id", event.ID, "stream_version", event.StreamVersion, "error", err)
				continue
			}

			return result, fmt.Errorf("processing event: %w", err)
		}

		result.NumProjectedEvents++
	}

	return result, nil
}

// A StreamProjectionOption is an option for configuring a StreamProjection.
type StreamProjectionOption func(*StreamProjection)

// WithContinueOnHandlerError sets whether to continue projecting events
// if an error occurs while handling any individual event.
//
// The default behavior is to stop projecting events if an error occurs.
func WithContinueOnHandlerError(shouldContinue bool) StreamProjectionOption {
	return func(p *StreamProjection) {
		p.continueOnHandlerError = shouldContinue
	}
}

// WithLogger sets the logger for the StreamProjection.
func WithLogger(log estoria.Logger) StreamProjectionOption {
	return func(p *StreamProjection) {
		p.log = log
	}
}
