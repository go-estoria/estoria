package estoria

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type EventStore interface {
	LoadEvents(ctx context.Context, aggregateID TypedID) ([]Event, error)
	SaveEvents(ctx context.Context, events ...Event) error
}

// An AggregateStore loads and saves aggregates using an EventStore.
type AggregateStore[E Entity] struct {
	Events EventStore

	mu                 sync.RWMutex
	newEntity          EntityFactory[E]
	eventDataFactories map[string]func() EventData

	deserializeEventData func([]byte, any) error
	serializeEventData   func(any) ([]byte, error)
}

func NewAggregateStore[E Entity](eventStore EventStore, entityFactory EntityFactory[E]) *AggregateStore[E] {
	return &AggregateStore[E]{
		Events:               eventStore,
		newEntity:            entityFactory,
		eventDataFactories:   make(map[string]func() EventData),
		deserializeEventData: json.Unmarshal,
		serializeEventData:   json.Marshal,
	}
}

// Allow allows an event type to be used with the aggregate store.
func (c *AggregateStore[E]) Allow(eventType string, eventDataFactory func() EventData) {
	c.eventDataFactories[eventType] = eventDataFactory
}

func (c *AggregateStore[E]) Create() *Aggregate[E] {
	data := c.newEntity()
	typ := fmt.Sprintf("%T", data)
	return &Aggregate[E]{
		id:   TypedID{Type: typ, ID: UUID(uuid.New())},
		data: data,
	}
}

// Load loads an aggregate by its ID.
func (c *AggregateStore[E]) Load(ctx context.Context, id TypedID) (*Aggregate[E], error) {
	slog.Default().WithGroup("aggregatestore").Debug("reading aggregate", "aggregate_id", id)
	c.mu.RLock()
	defer c.mu.RUnlock()

	slog.Debug("REACHED HERE")
	events, err := c.Events.LoadEvents(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading events: %w", err)
	}

	aggregate := &Aggregate[E]{
		id:   id,
		data: c.newEntity(),
	}

	for i, event := range events {
		eventData := c.eventDataFactories[event.ID().Type]()
		if err := c.deserializeEventData(event.RawData(), eventData); err != nil {
			return nil, fmt.Errorf("deserializing event data %d of %d: %w", i+1, len(events), err)
		}

		event.SetData(eventData)

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

	for i := range aggregate.UnsavedEvents {
		data, err := c.serializeEventData(aggregate.UnsavedEvents[i].Data())
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		aggregate.UnsavedEvents[i].SetRawData(data)
	}

	// assume to be atomic, for now (it's not)
	if err := c.Events.SaveEvents(ctx, aggregate.UnsavedEvents...); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	aggregate.UnsavedEvents = []Event{}

	return nil
}