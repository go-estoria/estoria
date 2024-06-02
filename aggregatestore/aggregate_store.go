package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/serde"
	"go.jetpack.io/typeid"
)

// An EventSourcedAggregateStore loads and saves aggregates using an EventStore.
type EventSourcedAggregateStore[E estoria.Entity] struct {
	EventReader estoria.EventStreamReader
	EventWriter estoria.EventStreamWriter

	NewEntity          estoria.EntityFactory[E]
	eventDataFactories map[string]func() estoria.EventData
	eventDataSerde     estoria.EventDataSerde

	log *slog.Logger
}

func New[E estoria.Entity](
	eventReader estoria.EventStreamReader,
	eventWriter estoria.EventStreamWriter,
	entityFactory estoria.EntityFactory[E],
	opts ...AggregateStoreOption[E],
) (*EventSourcedAggregateStore[E], error) {
	store := &EventSourcedAggregateStore[E]{
		EventReader:        eventReader,
		EventWriter:        eventWriter,
		NewEntity:          entityFactory,
		eventDataFactories: make(map[string]func() estoria.EventData),
		eventDataSerde:     nil,
		log:                slog.Default().WithGroup("aggregatestore"),
	}

	for _, opt := range opts {
		if err := opt(store); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}

	if store.eventDataSerde == nil {
		store.eventDataSerde = serde.JSONEventData{}
	}

	return store, nil
}

// Allow allows an event type to be used with the aggregate store.
func (s *EventSourcedAggregateStore[E]) Allow(prototypes ...estoria.EventData) {
	for _, prototype := range prototypes {
		s.eventDataFactories[prototype.EventType()] = prototype.New
	}
}

func (s *EventSourcedAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	entity := s.NewEntity()
	id, err := typeid.From(entity.EntityType(), "")
	if err != nil {
		return nil, fmt.Errorf("generating aggregate ID: %w", err)
	}

	aggregate := &estoria.Aggregate[E]{}
	aggregate.SetID(id)
	aggregate.SetEntity(entity)

	return aggregate, nil
}

// Load loads an aggregate by its ID.
func (s *EventSourcedAggregateStore[E]) Load(ctx context.Context, id typeid.AnyID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
	s.log.Debug("loading aggregate from event store", "aggregate_id", id)

	aggregate, err := s.NewAggregate()
	if err != nil {
		return nil, fmt.Errorf("creating new aggregate: %w", err)
	}

	aggregate.SetID(id)

	hydrateOpts := estoria.HydrateAggregateOptions{
		ToVersion: opts.ToVersion,
	}

	if err := s.Hydrate(ctx, aggregate, hydrateOpts); err != nil {
		return nil, fmt.Errorf("hydrating aggregate from version %d: %w", aggregate.Version(), err)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *EventSourcedAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.HydrateAggregateOptions) error {
	log := s.log.With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from event store", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	if aggregate == nil {
		return fmt.Errorf("aggregate is nil")
	} else if opts.ToVersion < 0 {
		return fmt.Errorf("invalid target version")
	}

	readOpts := estoria.ReadStreamOptions{
		Offset:    aggregate.Version(),
		Direction: estoria.Forward,
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
	if errors.Is(err, estoria.ErrStreamNotFound) {
		return estoria.ErrAggregateNotFound
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
		if err := s.eventDataSerde.Unmarshal(evt.Data(), eventData); err != nil {
			return fmt.Errorf("deserializing event data: %w", err)
		}

		if err := aggregate.Entity().ApplyEvent(ctx, eventData); err != nil {
			return fmt.Errorf("applying event: %w", err)
		}

		aggregate.SetVersion(aggregate.Version() + 1)
	}

	return nil
}

// Save saves an aggregate.
func (s *EventSourcedAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.SaveAggregateOptions) error {
	unsavedEvents := aggregate.UnsavedEvents()
	s.log.Debug("saving aggregate to event store", "aggregate_id", aggregate.ID(), "events", len(unsavedEvents))

	if len(unsavedEvents) == 0 {
		s.log.Debug("no events to save")
		return nil
	}

	toSave := make([]estoria.EventStoreEvent, len(unsavedEvents))
	for i, unsavedEvent := range unsavedEvents {
		data, err := s.eventDataSerde.Marshal(unsavedEvent.Data())
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		toSave[i] = &event{
			id:        unsavedEvent.ID(),
			streamID:  unsavedEvent.AggregateID(),
			timestamp: unsavedEvent.Timestamp(),
			data:      data,
		}
	}

	// assume to be atomic, for now (it's not)
	if err := s.EventWriter.AppendStream(ctx, aggregate.ID(), estoria.AppendStreamOptions{}, toSave); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	for _, unsavedEvent := range unsavedEvents {
		aggregate.QueueEventForApplication(unsavedEvent.Data())
	}

	aggregate.ClearUnsavedEvents()

	if !opts.SkipApply {
		for {
			err := aggregate.ApplyNext(ctx)
			if err != nil && !errors.Is(err, estoria.ErrNoUnappliedEvents) {
				return fmt.Errorf("applying event: %w", err)
			}
		}
	}

	return nil
}

type AggregateStoreOption[E estoria.Entity] func(*EventSourcedAggregateStore[E]) error

func WithEventDataSerde[E estoria.Entity](serde estoria.EventDataSerde) AggregateStoreOption[E] {
	return func(s *EventSourcedAggregateStore[E]) error {
		s.eventDataSerde = serde
		return nil
	}
}
