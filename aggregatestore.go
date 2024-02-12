package continuum

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type EventStore interface {
	LoadEvents(ctx context.Context, aggregateID AggregateID) ([]Event, error)
	SaveEvents(ctx context.Context, events ...Event) error
}

// An AggregateStore loads and saves aggregates using an EventStore.
type AggregateStore[E Entity] struct {
	Events EventStore

	mu        sync.RWMutex
	newEntity EntityFactory[E]
}

func NewAggregateStore[E Entity](eventStore EventStore, entityFactory EntityFactory[E]) *AggregateStore[E] {
	return &AggregateStore[E]{
		Events:    eventStore,
		newEntity: entityFactory,
	}
}

func (c *AggregateStore[E]) Create() *Aggregate[E] {
	data := c.newEntity()
	typ := fmt.Sprintf("%T", data)
	return &Aggregate[E]{
		id:   AggregateID{Type: typ, ID: UUID(uuid.New())},
		data: data,
	}
}

// Load loads an aggregate by its ID.
func (c *AggregateStore[E]) Load(ctx context.Context, id AggregateID) (*Aggregate[E], error) {
	slog.Default().WithGroup("aggregatestore").Debug("reading aggregate", "aggregate_id", id)
	c.mu.RLock()
	defer c.mu.RUnlock()

	events, err := c.Events.LoadEvents(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading events: %w", err)
	}

	aggregate := &Aggregate[E]{
		id:   id,
		data: c.newEntity(),
	}

	for i, event := range events {
		if err := aggregate.Apply(ctx, event); err != nil {
			return nil, fmt.Errorf("applying event %d of %d: %w", i+1, len(events), err)
		}
	}

	return aggregate, nil
}

// Save saves an aggregate.
func (c *AggregateStore[E]) Save(ctx context.Context, aggregate *Aggregate[E]) error {
	slog.Default().WithGroup("aggregatewriter").Debug("writing aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.UnsavedEvents))
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(aggregate.UnsavedEvents) == 0 {
		slog.Debug("no events to save")
		return nil
	}

	saved := []Event{}
	for _, event := range aggregate.UnsavedEvents {
		if err := c.Events.SaveEvents(ctx, event); err != nil {
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
