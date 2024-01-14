package aggregatewriter

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// EventStoreWriter is an AggregateWriter that writes aggregates to an event store.
type EventStoreWriter[D continuum.AggregateData] struct {
	EventStore continuum.EventStore
}

// WriteAggregate writes an aggregate to the event store.
func (r EventStoreWriter[D]) WriteAggregate(ctx context.Context, aggregate *continuum.Aggregate[D]) error {
	if len(aggregate.UnsavedEvents) == 0 {
		slog.Warn("saving aggregate containing no unsaved events", "aggregate_id", aggregate.ID)
		return nil
	}

	slog.Default().WithGroup("aggregatewriter").Debug("writing aggregate", "id", aggregate.ID, "events", len(aggregate.UnsavedEvents))

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
	return fmt.Sprintf("saving event %s: %s", e.FailedEvent.EventID(), e.Err)
}
