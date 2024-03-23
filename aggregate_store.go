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

type AppendStreamOptions struct {
	ExpectVersion int64
}

type ReadStreamOptions struct {
	FromVersion int64
	Count       int64
	Direction   int
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

func (s *AggregateStore[E]) AddHook(hookType HookType, hook func(context.Context, *Aggregate[E]) error) {
	if s.hooks == nil {
		s.hooks = make(map[HookType][]HookFunc[E])
	}

	s.hooks[hookType] = append(s.hooks[hookType], hook)
}

// Allow allows an event type to be used with the aggregate store.
func (s *AggregateStore[E]) Allow(prototypes ...EventData) {
	for _, prototype := range prototypes {
		s.eventDataFactories[prototype.EventType()] = prototype.New
	}
}

func (s *AggregateStore[E]) Create() *Aggregate[E] {
	data := s.newEntity()
	return &Aggregate[E]{
		id:     data.EntityID(),
		entity: data,
	}
}

// Load loads an aggregate by its ID.
func (s *AggregateStore[E]) Load(ctx context.Context, id typeid.AnyID) (*Aggregate[E], error) {
	s.log.Debug("loading aggregate", "aggregate_id", id)

	aggregate := &Aggregate[E]{
		id:     id,
		entity: s.newEntity(),
	}

	stream, err := s.EventReader.ReadStream(ctx, id, ReadStreamOptions{
		FromVersion: aggregate.Version() + 1,
	})
	if errors.Is(err, ErrStreamNotFound) {
		return nil, ErrAggregateNotFound
	} else if err != nil {
		return nil, fmt.Errorf("reading event stream: %w", err)
	}

	for {
		evt, err := stream.Next(ctx)
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("reading event: %w", err)
		}

		s.log.Debug("event data", "event_id", evt.ID(), "data", len(evt.Data()))

		newEventData, ok := s.eventDataFactories[evt.ID().Prefix()]
		if !ok {
			return nil, fmt.Errorf("no event data factory for event type %s", evt.ID().Prefix())
		}

		eventData := newEventData()
		if err := s.deserializeEventData(evt.Data(), &eventData); err != nil {
			return nil, fmt.Errorf("deserializing event data: %w", err)
		}

		if err := aggregate.entity.ApplyEvent(ctx, eventData); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}

		aggregate.version++
	}

	return aggregate, nil
}

// Save saves an aggregate.
func (s *AggregateStore[E]) Save(ctx context.Context, aggregate *Aggregate[E]) error {
	s.log.Debug("saving aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.unsavedEvents))

	if hooks, ok := s.hooks[BeforeSaveAggregate]; ok {
		for _, hook := range hooks {
			if err := hook(ctx, aggregate); err != nil {
				return fmt.Errorf("before-save aggregate hook: %w", err)
			}
		}
	}

	if len(aggregate.unsavedEvents) == 0 {
		s.log.Debug("no events to save")
		return nil
	}

	toSave := make([]Event, len(aggregate.unsavedEvents))
	for i, unsavedEvent := range aggregate.unsavedEvents {
		data, err := s.serializeEventData(unsavedEvent.data)
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		toSave[i] = &event{
			id:        unsavedEvent.id,
			streamID:  unsavedEvent.streamID,
			timestamp: unsavedEvent.timestamp,
			data:      data,
		}
	}

	// assume to be atomic, for now (it's not)
	if err := s.EventWriter.AppendStream(ctx, aggregate.ID(), AppendStreamOptions{}, toSave...); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	aggregate.unsavedEvents = []*unsavedEvent{}

	if hooks, ok := s.hooks[AfterSaveAggregate]; ok {
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
