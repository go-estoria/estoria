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
	Next(ctx context.Context) (EventStoreEvent, error)
}

type EventStreamReader interface {
	ReadStream(ctx context.Context, id typeid.AnyID, opts ReadStreamOptions) (EventStreamIterator, error)
}

type EventStreamWriter interface {
	AppendStream(ctx context.Context, id typeid.AnyID, opts AppendStreamOptions, events ...EventStoreEvent) error
}

type EventStore interface {
	EventStreamReader
	EventStreamWriter
}

type AppendStreamOptions struct {
	ExpectVersion int64
}

// ReadStreamOptions are options for reading an event stream.
type ReadStreamOptions struct {
	// Offset is the starting position in the stream (exclusive).
	Offset int64

	// Count is the number of events to read.
	Count int64

	// Direction is the direction to read the stream.
	Direction ReadStreamDirection
}

type ReadStreamDirection int

const (
	Forward ReadStreamDirection = iota
	Reverse
)

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

	NewEntity          EntityFactory[E]
	eventDataFactories map[string]func() EventData

	unmarshalEventData func([]byte, EventData) error
	marshalEventData   func(EventData) ([]byte, error)

	log *slog.Logger
}

func NewAggregateStore[E Entity](
	eventReader EventStreamReader,
	eventWriter EventStreamWriter,
	entityFactory EntityFactory[E],
) *AggregateStore[E] {
	return &AggregateStore[E]{
		EventReader:        eventReader,
		EventWriter:        eventWriter,
		NewEntity:          entityFactory,
		eventDataFactories: make(map[string]func() EventData),
		unmarshalEventData: func(b []byte, d EventData) error { return json.Unmarshal(b, d) },
		marshalEventData:   func(d EventData) ([]byte, error) { return json.Marshal(d) },
		log:                slog.Default().WithGroup("aggregatestore"),
	}
}

// Allow allows an event type to be used with the aggregate store.
func (s *AggregateStore[E]) Allow(prototypes ...EventData) {
	for _, prototype := range prototypes {
		s.eventDataFactories[prototype.EventType()] = prototype.New
	}
}

func (s *AggregateStore[E]) NewAggregate() (*Aggregate[E], error) {
	entity := s.NewEntity()
	id, err := typeid.From(entity.EntityType(), "")
	if err != nil {
		return nil, fmt.Errorf("generating aggregate ID: %w", err)
	}

	return &Aggregate[E]{
		id:     id,
		entity: entity,
	}, nil
}

// Load loads an aggregate by its ID.
func (s *AggregateStore[E]) Load(ctx context.Context, id typeid.AnyID, opts LoadAggregateOptions) (*Aggregate[E], error) {
	s.log.Debug("loading aggregate from event store", "aggregate_id", id)

	aggregate, err := s.NewAggregate()
	if err != nil {
		return nil, fmt.Errorf("creating new aggregate: %w", err)
	}

	aggregate.SetID(id)

	hydrateOpts := HydrateAggregateOptions{
		ToVersion: opts.ToVersion,
	}

	if err := s.Hydrate(ctx, aggregate, hydrateOpts); err != nil {
		return nil, fmt.Errorf("hydrating aggregate from version %d: %w", aggregate.Version(), err)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *AggregateStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateAggregateOptions) error {
	log := s.log.With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from event store", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	if aggregate == nil {
		return fmt.Errorf("aggregate is nil")
	} else if opts.ToVersion < 0 {
		return fmt.Errorf("invalid target version")
	}

	readOpts := ReadStreamOptions{
		Offset:    aggregate.Version(),
		Direction: Forward,
	}

	if opts.ToVersion > 0 {
		if aggregate.Version() == opts.ToVersion {
			log.Debug("aggregate already at target version, nothing to hydrate", "version", opts.ToVersion)
			return nil
		} else if aggregate.Version() > opts.ToVersion {
			return fmt.Errorf("cannot hydrate aggregate with greater version than target version")
		}

		readOpts.Count = opts.ToVersion - aggregate.Version()
	}

	// Load the aggregate's events.
	stream, err := s.EventReader.ReadStream(ctx, aggregate.ID(), readOpts)
	if errors.Is(err, ErrStreamNotFound) {
		return ErrAggregateNotFound
	} else if err != nil {
		return fmt.Errorf("reading event stream: %w", err)
	}

	// Apply the events to the aggregate.
	for i := 0; ; i++ {
		evt, err := stream.Next(ctx)
		if err == io.EOF {
			log.Debug("end of event stream", "events_read", i, "hydrated_version", aggregate.Version())
			break
		} else if err != nil {
			return fmt.Errorf("reading event: %w", err)
		}

		newEventData, ok := s.eventDataFactories[evt.ID().Prefix()]
		if !ok {
			return fmt.Errorf("no event data factory for event type %s", evt.ID().Prefix())
		}

		eventData := newEventData()
		if err := s.unmarshalEventData(evt.Data(), eventData); err != nil {
			return fmt.Errorf("deserializing event data: %w", err)
		}

		if err := aggregate.entity.ApplyEvent(ctx, eventData); err != nil {
			return fmt.Errorf("applying event: %w", err)
		}

		aggregate.version++
	}

	return nil
}

// Save saves an aggregate.
func (s *AggregateStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveAggregateOptions) error {
	s.log.Debug("saving aggregate to event store", "aggregate_id", aggregate.ID(), "events", len(aggregate.unsavedEvents))

	if len(aggregate.unsavedEvents) == 0 {
		s.log.Debug("no events to save")
		return nil
	}

	toSave := make([]EventStoreEvent, len(aggregate.unsavedEvents))
	for i, unsavedEvent := range aggregate.unsavedEvents {
		data, err := s.marshalEventData(unsavedEvent.data)
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		toSave[i] = &event{
			id:        unsavedEvent.id,
			streamID:  unsavedEvent.aggregateID,
			timestamp: unsavedEvent.timestamp,
			data:      data,
		}
	}

	// assume to be atomic, for now (it's not)
	if err := s.EventWriter.AppendStream(ctx, aggregate.ID(), AppendStreamOptions{}, toSave...); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	for _, unsavedEvent := range aggregate.unsavedEvents {
		aggregate.QueueEventForApplication(unsavedEvent.data)
	}

	aggregate.unsavedEvents = nil

	if !opts.SkipApply {
		if err := aggregate.applyUnappliedEvents(ctx); err != nil {
			return fmt.Errorf("applying unapplied events: %w", err)
		}
	}

	return nil
}

type LoadAggregateOptions struct {
	// ToVersion is the version to load the aggregate to.
	// Default: 0 (load to the latest version)
	ToVersion int64
}

type HydrateAggregateOptions struct {
	// ToVersion is the version to hydrate the aggregate to.
	// Default: 0 (hydrate to the latest version)
	ToVersion int64
}

type SaveAggregateOptions struct {
	// SkipApply skips applying the events to the entity.
	// This is useful in situations where it is desireable to delay the application of events,
	// such as when wrapping the aggregate store with additional functionality.
	SkipApply bool
}

var ErrStreamNotFound = errors.New("stream not found")

var ErrAggregateNotFound = errors.New("aggregate not found")
