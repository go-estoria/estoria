package estoria

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type EventStore interface {
	FindStream(ctx context.Context, aggregateID TypedID) (EventStream, error)
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
func (c *AggregateStore[E]) Allow(eventDataFactory func() EventData) {
	data := eventDataFactory()
	c.eventDataFactories[data.EventType()] = eventDataFactory
}

func (c *AggregateStore[E]) Create() *Aggregate[E] {
	data := c.newEntity()
	return &Aggregate[E]{
		id:   TypedID{Type: data.EntityID().Type, ID: UUID(uuid.New())},
		data: data,
	}
}

// Load loads an aggregate by its ID.
func (c *AggregateStore[E]) Load(ctx context.Context, id TypedID) (*Aggregate[E], error) {
	log := slog.Default().WithGroup("aggregatestore")
	log.Debug("reading aggregate", "aggregate_id", id)
	c.mu.RLock()
	defer c.mu.RUnlock()

	stream, err := c.Events.FindStream(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("finding event stream: %w", err)
	}

	aggregate := &Aggregate[E]{
		id:   id,
		data: c.newEntity(),
	}

	for {
		evt, err := stream.Next(ctx)
		if err != nil {
			if err == io.EOF {
				break
			}

			return nil, fmt.Errorf("reading event: %w", err)
		}

		rawEventData := evt.Data()
		if len(rawEventData) == 0 {
			slog.Warn("event has no data", "event_id", evt.ID())
			continue
		}

		newEventData, ok := c.eventDataFactories[evt.ID().Type]
		if !ok {
			return nil, fmt.Errorf("no event data factory for event type %s", evt.ID().Type)
		}

		eventData := newEventData()
		if err := c.deserializeEventData(rawEventData, &eventData); err != nil {
			return nil, fmt.Errorf("deserializing event data: %w", err)
		}

		log.Debug("event data", "event_id", evt.ID(), "data", eventData)

		if err := aggregate.apply(ctx, &event{
			id:          evt.ID(),
			aggregateID: evt.AggregateID(),
			timestamp:   evt.Timestamp(),
			data:        eventData,
			raw:         rawEventData,
		}); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	return aggregate, nil
}

// Save saves an aggregate.
func (c *AggregateStore[E]) Save(ctx context.Context, aggregate *Aggregate[E]) error {
	slog.Default().WithGroup("aggregatewriter").Debug("writing aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.unsavedEvents))
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(aggregate.unsavedEvents) == 0 {
		slog.Debug("no events to save")
		return nil
	}

	stream, err := c.Events.FindStream(ctx, aggregate.ID())
	if err != nil {
		return fmt.Errorf("finding event stream: %w", err)
	}

	toSave := make([]Event, len(aggregate.unsavedEvents))
	for i := range aggregate.unsavedEvents {
		data, err := c.serializeEventData(aggregate.unsavedEvents[i].data)
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		aggregate.unsavedEvents[i].raw = data
		toSave[i] = aggregate.unsavedEvents[i]
	}

	// assume to be atomic, for now (it's not)
	if err := stream.Append(ctx, toSave...); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	aggregate.unsavedEvents = []*event{}

	return nil
}
