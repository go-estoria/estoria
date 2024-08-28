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
// It loads and hydrates aggregates by reading events from the event store and applying
// them to the aggregate. It saves aggregates by appending events to the event store.
type EventSourcedStore[E estoria.Entity] struct {
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter

	newEntity             estoria.EntityFactory[E]
	entityEventPrototypes map[string]estoria.EntityEvent
	entityEventMarshaler  estoria.Marshaler[estoria.EntityEvent, *estoria.EntityEvent]

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
		eventReader:           eventStore,
		eventWriter:           eventStore,
		newEntity:             entityFactory,
		entityEventPrototypes: make(map[string]estoria.EntityEvent),
		entityEventMarshaler:  estoria.JSONMarshaler[estoria.EntityEvent]{},
		log:                   slog.Default().WithGroup("aggregatestore"),
	}

	// register entity event factories
	entity := store.newEntity(uuid.UUID{})
	for _, prototype := range entity.EventTypes() {
		if _, ok := store.entityEventPrototypes[prototype.EventType()]; ok {
			return nil, InitializeAggregateStoreError{
				Operation: "registering entity event prototype",
				Err: fmt.Errorf("duplicate event type %s for entity %T",
					prototype.EventType(),
					entity.EntityID().TypeName()),
			}
		}

		store.entityEventPrototypes[prototype.EventType()] = prototype
	}

	for _, opt := range opts {
		if err := opt(store); err != nil {
			return nil, InitializeAggregateStoreError{Operation: "applying option", Err: err}
		}
	}

	if store.eventReader == nil && store.eventWriter == nil {
		return nil, InitializeAggregateStoreError{Err: errors.New("no event stream reader or writer provided")}
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
		return nil, LoadAggregateError{AggregateID: id, Operation: "creating new aggregate", Err: err}
	}

	hydrateOpts := HydrateOptions{
		ToVersion: opts.ToVersion,
	}

	if err := s.Hydrate(ctx, aggregate, hydrateOpts); err != nil {
		return nil, LoadAggregateError{AggregateID: id, Operation: "hydrating aggregate", Err: err}
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *EventSourcedStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error {
	if s.eventReader == nil {
		return HydrateAggregateError{AggregateID: aggregate.ID(), Err: errors.New("event store has no event stream reader")}
	}

	if aggregate == nil {
		return HydrateAggregateError{Err: errors.New("aggregate is nil")}
	} else if opts.ToVersion < 0 {
		return HydrateAggregateError{AggregateID: aggregate.ID(), Err: errors.New("invalid target version")}
	}

	log := s.log.With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from event store", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	readOpts := eventstore.ReadStreamOptions{
		Offset:    aggregate.Version(),
		Direction: eventstore.Forward,
	}

	if opts.ToVersion > 0 {
		if v := aggregate.Version(); v == opts.ToVersion {
			log.Debug("aggregate already at target version, nothing to hydrate", "version", opts.ToVersion)
			return nil
		} else if v > opts.ToVersion {
			return HydrateAggregateError{
				AggregateID: aggregate.ID(),
				Err:         fmt.Errorf("aggregate is at more recent version (%d) than requested version (%d)", v, opts.ToVersion),
			}
		}

		readOpts.Count = opts.ToVersion - aggregate.Version()
	}

	// load the aggregate's events
	stream, err := s.eventReader.ReadStream(ctx, aggregate.ID(), readOpts)
	if errors.Is(err, eventstore.StreamNotFoundError{}) {
		return AggregateNotFoundError{AggregateID: aggregate.ID()}
	} else if err != nil {
		return HydrateAggregateError{AggregateID: aggregate.ID(), Operation: "reading event stream", Err: err}
	}

	defer stream.Close(ctx)

	// apply the events to the aggregate
	for numRead := 0; ; numRead++ {
		event, err := stream.Next(ctx)
		if errors.Is(err, io.EOF) {
			log.Debug("end of event stream", "events_read", numRead)
			break
		} else if err != nil {
			return HydrateAggregateError{AggregateID: aggregate.ID(), Operation: "reading event", Err: err}
		}

		prototype, ok := s.entityEventPrototypes[event.ID.TypeName()]
		if !ok {
			return HydrateAggregateError{
				AggregateID: aggregate.ID(),
				Operation:   "obtaining entity prototype",
				Err:         errors.New("no prototype registered for event type %s" + event.ID.TypeName()),
			}
		}

		entityEvent := prototype.New()
		if err := s.entityEventMarshaler.Unmarshal(event.Data, &entityEvent); err != nil {
			return HydrateAggregateError{AggregateID: aggregate.ID(), Operation: "unmarshaling event data", Err: err}
		}

		// enqueue and apply the event immediately
		aggregate.State().WillApply(&AggregateEvent{
			ID:          event.ID,
			Version:     event.StreamVersion,
			Timestamp:   event.Timestamp,
			EntityEvent: entityEvent,
		})
		if err := aggregate.State().ApplyNext(ctx); errors.Is(err, ErrNoUnappliedEvents) {
			return HydrateAggregateError{
				AggregateID: aggregate.ID(),
				Operation:   "applying event",
				Err:         errors.New("unexpected end of unapplied events while hydrating aggregate"),
			}
		} else if err != nil {
			return HydrateAggregateError{AggregateID: aggregate.ID(), Operation: "applying event", Err: err}
		}
	}

	s.log.Debug("hydrated aggregate",
		"aggregate_id", aggregate.ID(),
		"entity_id", aggregate.Entity().EntityID(),
		"version", aggregate.Version())

	return nil
}

// Save saves an aggregate.
func (s *EventSourcedStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	if s.eventWriter == nil {
		return SaveAggregateError{AggregateID: aggregate.ID(), Err: errors.New("event store has no event stream writer")}
	}

	unsavedEvents := aggregate.State().UnsavedEvents()
	s.log.Debug("saving aggregate to event store", "aggregate_id", aggregate.ID(), "events", len(unsavedEvents))

	if len(unsavedEvents) == 0 {
		s.log.Debug("no events to save")
		return nil
	}

	events := make([]*eventstore.WritableEvent, len(unsavedEvents))

	for i, unsavedEvent := range unsavedEvents {
		nextVersion := aggregate.Version() + int64(i) + 1
		if unsavedEvent.Version > 0 && unsavedEvent.Version != nextVersion {
			return SaveAggregateError{
				AggregateID: aggregate.ID(),
				Err: fmt.Errorf("event version mismatch: next is %d, event specifies %d",
					nextVersion, unsavedEvent.Version),
			}
		}

		data, err := s.entityEventMarshaler.Marshal(&unsavedEvent.EntityEvent)
		if err != nil {
			return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "serializing event data", Err: err}
		}

		events[i] = &eventstore.WritableEvent{
			ID:   unsavedEvent.ID,
			Data: data,
		}
	}

	// write to event stream
	if err := s.eventWriter.AppendStream(ctx, aggregate.ID(), events, eventstore.AppendStreamOptions{
		ExpectVersion: aggregate.Version(),
	}); err != nil {
		return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "saving events to stream", Err: err}
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
			return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "applying event", Err: err}
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
