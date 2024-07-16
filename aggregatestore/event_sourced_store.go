package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

// An EventSourcedStore loads and saves aggregates using an EventStore.
// It hydrates aggregates by reading events from the event store and applying them to the aggregate.
type EventSourcedStore[E estoria.Entity] struct {
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter

	newEntity            estoria.EntityFactory[E]
	eventDataFactories   map[string]func() estoria.EntityEvent
	entityEventMarshaler estoria.Marshaler[estoria.EntityEvent, *estoria.EntityEvent]

	log *slog.Logger
}

var _ Store[estoria.Entity] = (*EventSourcedStore[estoria.Entity])(nil)

func NewEventSourcedStore[E estoria.Entity](
	eventStore eventstore.Store,
	entityFactory estoria.EntityFactory[E],
	opts ...EventSourcedAggregateStoreOption[E],
) (*EventSourcedStore[E], error) {
	store := &EventSourcedStore[E]{
		eventReader:          eventStore,
		eventWriter:          eventStore,
		newEntity:            entityFactory,
		eventDataFactories:   make(map[string]func() estoria.EntityEvent),
		entityEventMarshaler: estoria.JSONMarshaler[estoria.EntityEvent]{},
		log:                  slog.Default().WithGroup("aggregatestore"),
	}

	for _, prototype := range store.newEntity().EventTypes() {
		if _, ok := store.eventDataFactories[prototype.EventType()]; ok {
			return nil, fmt.Errorf("duplicate event type %s for entity %T",
				prototype.EventType(),
				store.newEntity().EntityID().TypeName())
		}

		store.eventDataFactories[prototype.EventType()] = prototype.New
	}

	for _, opt := range opts {
		if err := opt(store); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}

	if store.eventReader == nil && store.eventWriter == nil {
		return nil, errors.New("no event store reader or writer provided")
	}

	return store, nil
}

func (s *EventSourcedStore[E]) New(id *typeid.UUID) (*estoria.Aggregate[E], error) {
	entity := s.newEntity()
	if id != nil {
		entity.SetEntityID(*id)
	}

	aggregate := &estoria.Aggregate[E]{}
	aggregate.SetEntity(entity)

	s.log.Debug("created new aggregate", "aggregate_id", aggregate.ID())
	return aggregate, nil
}

// Load loads an aggregate by its ID.
func (s *EventSourcedStore[E]) Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*estoria.Aggregate[E], error) {
	s.log.Debug("loading aggregate from event store", "aggregate_id", id)

	aggregate, err := s.New(&id)
	if err != nil {
		return nil, fmt.Errorf("creating new aggregate: %w", err)
	}

	hydrateOpts := HydrateOptions{
		ToVersion: opts.ToVersion,
	}

	if err := s.Hydrate(ctx, aggregate, hydrateOpts); err != nil {
		return nil, fmt.Errorf("hydrating aggregate from version %d: %w", aggregate.Version(), err)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *EventSourcedStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E], opts HydrateOptions) error {
	log := s.log.With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from event store", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	if aggregate == nil {
		return fmt.Errorf("aggregate is nil")
	} else if opts.ToVersion < 0 {
		return fmt.Errorf("invalid target version")
	}

	if aggregate.Version() == 0 {
		aggregate.Entity().SetEntityID(aggregate.ID())
	}

	readOpts := eventstore.ReadStreamOptions{
		Offset:    aggregate.Version(),
		Direction: eventstore.Forward,
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
	stream, err := s.eventReader.ReadStream(ctx, aggregate.ID(), readOpts)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
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

		newEntityEvent, ok := s.eventDataFactories[evt.ID.TypeName()]
		if !ok {
			return fmt.Errorf("no entity event factory for event type %s", evt.ID.TypeName())
		}

		entityEvent := newEntityEvent()
		if err := s.entityEventMarshaler.Unmarshal(evt.Data, &entityEvent); err != nil {
			return fmt.Errorf("deserializing event data: %w", err)
		}

		// queue and apply the event immediately
		aggregate.AddForApply(entityEvent)
		if err := aggregate.ApplyNext(ctx); errors.Is(err, estoria.ErrNoUnappliedEvents) {
			break
		} else if err != nil {
			return fmt.Errorf("applying event: %w", err)
		}
	}

	s.log.Debug("hydrated aggregate", "aggregate_id", aggregate.ID(), "entity_id", aggregate.Entity().EntityID(), "version", aggregate.Version())

	return nil
}

// Save saves an aggregate.
func (s *EventSourcedStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts SaveOptions) error {
	unsavedEvents := aggregate.UnsavedEvents()
	s.log.Debug("saving aggregate to event store", "aggregate_id", aggregate.ID(), "events", len(unsavedEvents))

	if len(unsavedEvents) == 0 {
		s.log.Debug("no events to save")
		return nil
	}

	events := make([]*eventstore.EventStoreEvent, len(unsavedEvents))
	for i, unsavedEvent := range unsavedEvents {
		data, err := s.entityEventMarshaler.Marshal(&unsavedEvent.EntityEvent)
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		events[i] = &eventstore.EventStoreEvent{
			ID:        unsavedEvent.ID,
			StreamID:  aggregate.ID(),
			Timestamp: unsavedEvent.Timestamp,
			Data:      data,
		}
	}

	// write to event stream
	if err := s.eventWriter.AppendStream(ctx, aggregate.ID(), eventstore.AppendStreamOptions{
		ExpectVersion: aggregate.Version(),
	}, events); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	// queue the events for application
	for _, unsavedEvent := range unsavedEvents {
		aggregate.AddForApply(unsavedEvent.EntityEvent)
	}

	aggregate.ClearUnsavedEvents()

	if opts.SkipApply {
		return nil
	}

	// apply the events to the aggregate
	for {
		if err := aggregate.ApplyNext(ctx); errors.Is(err, estoria.ErrNoUnappliedEvents) {
			return nil
		} else if err != nil {
			return fmt.Errorf("applying event: %w", err)
		}
	}
}

type EventSourcedAggregateStoreOption[E estoria.Entity] func(*EventSourcedStore[E]) error

func WithEntityEventMarshaler[E estoria.Entity](marshaler estoria.Marshaler[estoria.EntityEvent, *estoria.EntityEvent]) EventSourcedAggregateStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.entityEventMarshaler = marshaler
		return nil
	}
}

func WithStreamReader[E estoria.Entity](reader eventstore.StreamReader) EventSourcedAggregateStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.eventReader = reader
		return nil
	}
}

func WithStreamWriter[E estoria.Entity](writer eventstore.StreamWriter) EventSourcedAggregateStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.eventWriter = writer
		return nil
	}
}
