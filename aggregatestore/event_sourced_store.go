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
	"github.com/gofrs/uuid/v5"
)

// An EventSourcedStore loads and saves aggregates using an EventStore.
// It hydrates aggregates by reading events from the event store and applying them to the aggregate.
type EventSourcedStore[E estoria.Entity] struct {
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter

	newEntity            estoria.EntityFactory[E]
	entityEventFactories map[string]func() estoria.EntityEvent
	entityEventMarshaler estoria.Marshaler[estoria.EntityEvent, *estoria.EntityEvent]

	log *slog.Logger
}

var _ Store[estoria.Entity] = (*EventSourcedStore[estoria.Entity])(nil)

// NewEventSourcedStore creates a new EventSourcedStore.
func NewEventSourcedStore[E estoria.Entity](
	eventStore eventstore.Store,
	entityFactory estoria.EntityFactory[E],
	opts ...EventSourcedStoreOption[E],
) (*EventSourcedStore[E], error) {
	store := &EventSourcedStore[E]{
		eventReader:          eventStore,
		eventWriter:          eventStore,
		newEntity:            entityFactory,
		entityEventFactories: make(map[string]func() estoria.EntityEvent),
		entityEventMarshaler: estoria.JSONMarshaler[estoria.EntityEvent]{},
		log:                  slog.Default().WithGroup("aggregatestore"),
	}

	entity := store.newEntity(uuid.UUID{})
	for _, prototype := range entity.EventTypes() {
		if _, ok := store.entityEventFactories[prototype.EventType()]; ok {
			return nil, fmt.Errorf("duplicate event type %s for entity %T",
				prototype.EventType(),
				entity.EntityID().TypeName())
		}

		store.entityEventFactories[prototype.EventType()] = prototype.New
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

// New creates a new aggregate.
// If an ID is provided, the aggregate is created with that ID.
func (s *EventSourcedStore[E]) New(id uuid.UUID) (*Aggregate[E], error) {
	aggregate := &Aggregate[E]{}
	aggregate.State().SetEntityAtVersion(s.newEntity(id), 0)

	s.log.Debug("created new aggregate", "aggregate_id", aggregate.ID())
	return aggregate, nil
}

// Load loads an aggregate by its ID.
func (s *EventSourcedStore[E]) Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*Aggregate[E], error) {
	s.log.Debug("loading aggregate from event store", "aggregate_id", id)

	aggregate, err := s.New(id.UUID())
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
func (s *EventSourcedStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error {
	log := s.log.With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from event store", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	if aggregate == nil {
		return fmt.Errorf("aggregate is nil")
	} else if opts.ToVersion < 0 {
		return fmt.Errorf("invalid target version")
	}

	readOpts := eventstore.ReadStreamOptions{
		Offset:    aggregate.Version(),
		Direction: eventstore.Forward,
	}

	if opts.ToVersion > 0 {
		if aggregate.Version() == opts.ToVersion {
			log.Debug("aggregate already at target version, nothing to hydrate", "version", opts.ToVersion)
			return nil
		} else if v := aggregate.Version(); v > opts.ToVersion {
			return fmt.Errorf("cannot hydrate aggregate to lower version (%d) than current version (%d)", opts.ToVersion, v)
		}

		readOpts.Count = opts.ToVersion - aggregate.Version()
	}

	// load the aggregate's events
	stream, err := s.eventReader.ReadStream(ctx, aggregate.ID(), readOpts)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return ErrAggregateNotFound
	} else if err != nil {
		return fmt.Errorf("reading event stream: %w", err)
	}

	defer stream.Close(ctx)

	// apply the events to the aggregate
	for numRead := 0; ; numRead++ {
		evt, err := stream.Next(ctx)
		if err == io.EOF {
			log.Debug("end of event stream", "events_read", numRead)
			break
		} else if err != nil {
			return fmt.Errorf("reading event: %w", err)
		}

		newEntityEvent, ok := s.entityEventFactories[evt.ID.TypeName()]
		if !ok {
			return fmt.Errorf("no entity event factory for event type %s", evt.ID.TypeName())
		}

		entityEvent := newEntityEvent()
		if err := s.entityEventMarshaler.Unmarshal(evt.Data, &entityEvent); err != nil {
			return fmt.Errorf("deserializing event data: %w", err)
		}

		// enqueue and apply the event immediately
		aggregate.State().WillApply(&AggregateEvent{
			ID:          evt.ID,
			Version:     evt.StreamVersion,
			Timestamp:   evt.Timestamp,
			EntityEvent: entityEvent,
		})
		if err := aggregate.State().ApplyNext(ctx); errors.Is(err, ErrNoUnappliedEvents) {
			return fmt.Errorf("unexpected end of event stream while hydrating aggregate")
		} else if err != nil {
			return fmt.Errorf("applying event: %w", err)
		}
	}

	s.log.Debug("hydrated aggregate", "aggregate_id", aggregate.ID(), "entity_id", aggregate.Entity().EntityID(), "version", aggregate.Version())

	return nil
}

// Save saves an aggregate.
func (s *EventSourcedStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	unsavedEvents := aggregate.State().UnsavedEvents()
	s.log.Debug("saving aggregate to event store", "aggregate_id", aggregate.ID(), "events", len(unsavedEvents))

	if len(unsavedEvents) == 0 {
		s.log.Debug("no events to save")
		return nil
	}

	events := make([]*eventstore.Event, len(unsavedEvents))
	for i, unsavedEvent := range unsavedEvents {
		nextVersion := aggregate.Version() + int64(i) + 1

		if unsavedEvent.Version > 0 && unsavedEvent.Version != nextVersion {
			return fmt.Errorf("event version mismatch: next is %d, event specifies %d",
				aggregate.Version()+int64(i)+1,
				unsavedEvent.Version)
		}

		data, err := s.entityEventMarshaler.Marshal(&unsavedEvent.EntityEvent)
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		events[i] = &eventstore.Event{
			ID:            unsavedEvent.ID,
			StreamID:      aggregate.ID(),
			StreamVersion: nextVersion,
			Timestamp:     unsavedEvent.Timestamp,
			Data:          data,
		}
	}

	// write to event stream
	if err := s.eventWriter.AppendStream(ctx, aggregate.ID(), events, eventstore.AppendStreamOptions{
		ExpectVersion: aggregate.Version(),
	}); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	// queue the events for application
	for _, unsavedEvent := range unsavedEvents {
		aggregate.State().WillApply(unsavedEvent)
	}

	aggregate.State().ClearUnsavedEvents()

	if opts.SkipApply {
		return nil
	}

	// apply the events to the aggregate
	for {
		if err := aggregate.State().ApplyNext(ctx); errors.Is(err, ErrNoUnappliedEvents) {
			return nil
		} else if err != nil {
			return fmt.Errorf("applying event: %w", err)
		}
	}
}

type EventSourcedStoreOption[E estoria.Entity] func(*EventSourcedStore[E]) error

func WithEntityEventMarshaler[E estoria.Entity](marshaler estoria.Marshaler[estoria.EntityEvent, *estoria.EntityEvent]) EventSourcedStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.entityEventMarshaler = marshaler
		return nil
	}
}

func WithStreamReader[E estoria.Entity](reader eventstore.StreamReader) EventSourcedStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.eventReader = reader
		return nil
	}
}

func WithStreamWriter[E estoria.Entity](writer eventstore.StreamWriter) EventSourcedStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.eventWriter = writer
		return nil
	}
}
