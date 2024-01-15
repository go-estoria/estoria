package aggregatewriter

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// An EventSaver is used by the AggregateWriter to save events to an event store.
type EventSaver interface {
	SaveEvent(ctx context.Context, event continuum.Event) error
}

// EventStoreWriter is an AggregateWriter that writes aggregates to an event store.
type EventStoreWriter struct {
	EventStore EventSaver
}

// WriteAggregate writes an aggregate to the event store.
func (r EventStoreWriter) WriteAggregate(ctx context.Context, aggregate *continuum.Aggregate) error {
	slog.Default().WithGroup("aggregatewriter").Debug("writing aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.UnsavedEvents))

	if len(aggregate.UnsavedEvents) == 0 {
		return nil
	}

	saved := []continuum.Event{}
	for _, event := range aggregate.UnsavedEvents {
		if err := r.EventStore.SaveEvent(ctx, event); err != nil {
			return ErrEventSaveFailed{
				Err:         err,
				FailedEvent: event,
				SavedEvents: saved,
			}
		}

		saved = append(saved, event)
	}

	aggregate.UnsavedEvents = []continuum.Event{}

	return nil
}

type ErrEventSaveFailed struct {
	Err         error
	FailedEvent continuum.Event
	SavedEvents []continuum.Event
}

func (e ErrEventSaveFailed) Error() string {
	return fmt.Sprintf("saving event %s: %s", e.FailedEvent.ID(), e.Err)
}
