package memory

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

type EventStore interface {
	LoadEvents(ctx context.Context, aggregateID continuum.AggregateID) ([]continuum.Event, error)
	SaveEvent(ctx context.Context, event continuum.Event) error
}

// An AggregateStore loads and saves aggregates.
type AggregateStore struct {
	Events EventStore
}

func NewAggregateStore(eventStore EventStore) (*AggregateStore, error) {
	return &AggregateStore{
		Events: eventStore,
	}, nil
}

func (c *AggregateStore) Load(ctx context.Context, id continuum.AggregateID) (*continuum.Aggregate, error) {
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

func (c *AggregateStore) Save(ctx context.Context, aggregate *continuum.Aggregate) error {
	slog.Default().WithGroup("aggregatewriter").Debug("writing aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.UnsavedEvents))

	if len(aggregate.UnsavedEvents) == 0 {
		return nil
	}

	saved := []continuum.Event{}
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
