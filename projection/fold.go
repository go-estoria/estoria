package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
)

// A Fold reads events from an event stream and executes a projection function for each event.
type Fold struct {
	iter                   eventstore.StreamIterator
	continueOnHandlerError bool

	log estoria.Logger
}

// NewFold creates a new Fold.
func NewFold(iter eventstore.StreamIterator, opts ...FoldOption) (*Fold, error) {
	if iter == nil {
		return nil, errors.New("event stream iterator is required")
	}

	fold := &Fold{
		iter: iter,
		log:  estoria.GetLogger().WithGroup("projection"),
	}

	for _, opt := range opts {
		opt(fold)
	}

	return fold, nil
}

// Result contains the result of a projection.
type Result struct {
	// NumProjectedEvents is the number of events that were successfully projected.
	NumProjectedEvents int64

	// NumFailedEvents is the number of events that failed to project.
	NumFailedEvents int64
}

func (p *Fold) Project(ctx context.Context, eventHandler EventHandler) (*Result, error) {
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

// A FoldOption is an option for configuring a Fold.
type FoldOption func(*Fold)

// WithContinueOnHandlerError sets whether to continue projecting events
// if an error occurs while handling any individual event.
//
// The default behavior is to stop projecting events if an error occurs.
func WithContinueOnHandlerError(shouldContinue bool) FoldOption {
	return func(p *Fold) {
		p.continueOnHandlerError = shouldContinue
	}
}

// WithLogger sets the logger for the Fold.
func WithLogger(log estoria.Logger) FoldOption {
	return func(p *Fold) {
		p.log = log
	}
}
