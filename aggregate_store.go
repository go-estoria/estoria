package estoria

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
)

type EventStreamIterator interface {
	Next(ctx context.Context) (Event, error)
}

type EventStreamReader interface {
	ReadStream(ctx context.Context, id Identifier, opts ReadStreamOptions) (EventStreamIterator, error)
}

type EventStreamWriter interface {
	AppendStream(ctx context.Context, id Identifier, opts AppendStreamOptions, events ...Event) error
}

type AppendStreamOptions struct{}

type ReadStreamOptions struct{}

// An AggregateStore loads and saves aggregates using an EventStore.
type AggregateStore[E Entity] struct {
	EventReader EventStreamReader
	EventWriter EventStreamWriter

	newEntity          EntityFactory[E]
	eventDataFactories map[string]func() EventData

	deserializeEventData func([]byte, any) error
	serializeEventData   func(any) ([]byte, error)
}

func NewAggregateStore[E Entity](
	eventReader EventStreamReader,
	eventWriter EventStreamWriter,
	entityFactory EntityFactory[E],
) *AggregateStore[E] {
	return &AggregateStore[E]{
		EventReader:          eventReader,
		EventWriter:          eventWriter,
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

	stream, err := c.EventReader.ReadStream(ctx, id.ID, ReadStreamOptions{})
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

	if len(aggregate.unsavedEvents) == 0 {
		slog.Debug("no events to save")
		return nil
	}

	toSave := make([]Event, len(aggregate.unsavedEvents))
	for i := range toSave {
		data, err := c.serializeEventData(aggregate.unsavedEvents[i].data)
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		aggregate.unsavedEvents[i].raw = data
		toSave[i] = aggregate.unsavedEvents[i]
	}

	// assume to be atomic, for now (it's not)
	if err := c.EventWriter.AppendStream(ctx, aggregate.ID().ID, AppendStreamOptions{}, toSave...); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	aggregate.unsavedEvents = []*event{}

	return nil
}

var ErrStreamNotFound = errors.New("stream not found")

var ErrAggregateNotFound = errors.New("aggregate not found")
