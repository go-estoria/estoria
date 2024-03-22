package estoria

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"go.jetpack.io/typeid"
)

type EventStreamIterator interface {
	Next(ctx context.Context) (Event, error)
}

type EventStreamReader interface {
	ReadStream(ctx context.Context, id typeid.AnyID, opts ReadStreamOptions) (EventStreamIterator, error)
}

type EventStreamWriter interface {
	AppendStream(ctx context.Context, id typeid.AnyID, opts AppendStreamOptions, events ...Event) error
}

type AppendStreamOptions struct{}

type ReadStreamOptions struct {
	FromVersion int64
	ToVersion   int64
}

type HookType int

const (
	BeforeLoadAggregate HookType = iota
	AfterLoadAggregate
	BeforeSaveAggregate
	AfterSaveAggregate
)

type HookFunc[E Entity] func(context.Context, *Aggregate[E]) error

// An AggregateStore loads and saves aggregates using an EventStore.
type AggregateStore[E Entity] struct {
	EventReader EventStreamReader
	EventWriter EventStreamWriter

	newEntity          EntityFactory[E]
	eventDataFactories map[string]func() EventData

	deserializeEventData func([]byte, any) error
	serializeEventData   func(any) ([]byte, error)

	hooks map[HookType][]HookFunc[E]

	log *slog.Logger
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
		log:                  slog.Default().WithGroup("aggregatestore"),
	}
}

func (c *AggregateStore[E]) AddHook(hookType HookType, hook func(context.Context, *Aggregate[E]) error) {
	if c.hooks == nil {
		c.hooks = make(map[HookType][]HookFunc[E])
	}

	c.hooks[hookType] = append(c.hooks[hookType], hook)
}

// Allow allows an event type to be used with the aggregate store.
func (c *AggregateStore[E]) Allow(eventDataFactory func() EventData) {
	data := eventDataFactory()
	c.eventDataFactories[data.EventType()] = eventDataFactory
}

func (c *AggregateStore[E]) Create() *Aggregate[E] {
	data := c.newEntity()
	return &Aggregate[E]{
		id:   data.EntityID(),
		data: data,
	}
}

// Load loads an aggregate by its ID.
func (c *AggregateStore[E]) Load(ctx context.Context, id typeid.AnyID) (*Aggregate[E], error) {
	c.log.Debug("loading aggregate", "aggregate_id", id)

	aggregate := &Aggregate[E]{
		id:   id,
		data: c.newEntity(),
	}

	if hooks, ok := c.hooks[BeforeLoadAggregate]; ok {
		for _, hook := range hooks {
			if err := hook(ctx, aggregate); err != nil {
				return nil, fmt.Errorf("before-load aggregate hook: %w", err)
			}
		}
	}

	stream, err := c.EventReader.ReadStream(ctx, id, ReadStreamOptions{
		FromVersion: aggregate.Version() + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("finding event stream: %w", err)
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
			c.log.Warn("event has no data", "event_id", evt.ID())
			continue
		}

		newEventData, ok := c.eventDataFactories[evt.ID().Prefix()]
		if !ok {
			return nil, fmt.Errorf("no event data factory for event type %s", evt.ID().Prefix())
		}

		eventData := newEventData()
		if err := c.deserializeEventData(rawEventData, &eventData); err != nil {
			return nil, fmt.Errorf("deserializing event data: %w", err)
		}

		c.log.Debug("event data", "event_id", evt.ID(), "data", eventData)

		if err := aggregate.apply(ctx, &event{
			id:        evt.ID(),
			streamID:  evt.StreamID(),
			timestamp: evt.Timestamp(),
			data:      eventData,
			raw:       rawEventData,
		}); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	if hooks, ok := c.hooks[AfterLoadAggregate]; ok {
		for _, hook := range hooks {
			if err := hook(ctx, aggregate); err != nil {
				return nil, fmt.Errorf("after-load aggregate hook: %w", err)
			}
		}
	}

	return aggregate, nil
}

// Save saves an aggregate.
func (c *AggregateStore[E]) Save(ctx context.Context, aggregate *Aggregate[E]) error {
	c.log.Debug("saving aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.unsavedEvents))

	if hooks, ok := c.hooks[BeforeSaveAggregate]; ok {
		for _, hook := range hooks {
			if err := hook(ctx, aggregate); err != nil {
				return fmt.Errorf("before-save aggregate hook: %w", err)
			}
		}
	}

	if len(aggregate.unsavedEvents) == 0 {
		c.log.Debug("no events to save")
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
	if err := c.EventWriter.AppendStream(ctx, aggregate.ID(), AppendStreamOptions{}, toSave...); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	aggregate.unsavedEvents = []*event{}

	if hooks, ok := c.hooks[AfterSaveAggregate]; ok {
		for _, hook := range hooks {
			if err := hook(ctx, aggregate); err != nil {
				return fmt.Errorf("after-save aggregate hook: %w", err)
			}
		}
	}

	return nil
}

var ErrStreamNotFound = errors.New("stream not found")

var ErrAggregateNotFound = errors.New("aggregate not found")
