package continuum

import (
	"context"
	"fmt"
	"log/slog"
)

type EventStore interface {
	LoadEvents(ctx context.Context, aggregateID AggregateID) ([]Event, error)
	SaveEvent(ctx context.Context, event Event) error
}

// An AggregateStore loads and saves aggregates.
type AggregateStore struct {
	Events EventStore
}

func (c *AggregateStore) Load(ctx context.Context, id AggregateID) (*Aggregate, error) {
	slog.Default().WithGroup("aggregatestore").Debug("reading aggregate", "aggregate_id", id)
	events, err := c.Events.LoadEvents(ctx, id)
	if err != nil {
		return nil, err
	}

	aggregate := id.NewAggregate()

	for _, event := range events {
		if err := aggregate.Apply(ctx, event); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	return aggregate, nil
}

func (c *AggregateStore) Save(ctx context.Context, aggregate *Aggregate) error {
	slog.Default().WithGroup("aggregatewriter").Debug("writing aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.UnsavedEvents))

	if len(aggregate.UnsavedEvents) == 0 {
		return nil
	}

	saved := []Event{}
	for _, event := range aggregate.UnsavedEvents {
		if err := c.Events.SaveEvent(ctx, event); err != nil {
			return ErrEventSaveFailed{
				Err:         err,
				FailedEvent: event,
				SavedEvents: saved,
			}
		}

		saved = append(saved, event)
	}

	aggregate.UnsavedEvents = []Event{}

	return nil
}

type ErrEventSaveFailed struct {
	Err         error
	FailedEvent Event
	SavedEvents []Event
}

func (e ErrEventSaveFailed) Error() string {
	return fmt.Sprintf("saving event %s: %s", e.FailedEvent.ID(), e.Err)
}
