package aggregatestore

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/projection"
	"github.com/gofrs/uuid/v5"
)

// An EventSourcedStore loads and saves aggregates using an EventStore.
// It loads and hydrates aggregates by reading events from the event store and applying
// them to the aggregate. It saves aggregates by appending events to the event store.
type EventSourcedStore[E estoria.Entity] struct {
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter

	newEntity             estoria.EntityFactory[E]
	entityEventPrototypes map[string]func() estoria.EntityEvent[E]
	entityEventMarshaler  estoria.EntityEventMarshaler[E]

	log estoria.Logger
}

var _ Store[estoria.Entity] = (*EventSourcedStore[estoria.Entity])(nil)

// New creates a new event sourced aggregate store.
func New[E estoria.Entity](
	eventStore eventstore.Store,
	entityFactory estoria.EntityFactory[E],
	opts ...EventSourcedStoreOption[E],
) (*EventSourcedStore[E], error) {
	store := &EventSourcedStore[E]{
		eventReader:           eventStore,
		eventWriter:           eventStore,
		newEntity:             entityFactory,
		entityEventPrototypes: make(map[string]func() estoria.EntityEvent[E]),
		entityEventMarshaler:  estoria.JSONEntityEventMarshaler[E]{},
		log:                   estoria.GetLogger().WithGroup("eventsourcedstore"),
	}

	for _, opt := range opts {
		if err := opt(store); err != nil {
			return nil, InitializeError{Operation: "applying option", Err: err}
		}
	}

	if store.eventReader == nil && store.eventWriter == nil {
		return nil, InitializeError{Err: errors.New("no event stream reader or writer provided")}
	}

	return store, nil
}

func (s *EventSourcedStore[E]) New(id uuid.UUID) *Aggregate[E] {
	return NewAggregate(s.newEntity(id), 0)
}

// Load loads an aggregate by its ID.
func (s *EventSourcedStore[E]) Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[E], error) {
	if id == uuid.Nil {
		return nil, LoadError{Err: errors.New("aggregate ID is nil")}
	}

	s.log.Debug("loading aggregate from event store", "aggregate_id", id)

	aggregate := s.New(id)

	if opts == nil {
		opts = &LoadOptions{}
	} else if err := opts.Validate(); err != nil {
		return nil, LoadError{AggregateID: aggregate.ID(), Err: fmt.Errorf("invalid load options: %w", err)}
	}

	hydrateOpts := HydrateOptions{
		ToVersion: opts.ToVersion,
	}

	if err := s.Hydrate(ctx, aggregate, &hydrateOpts); err != nil {
		return nil, LoadError{AggregateID: aggregate.ID(), Operation: "hydrating aggregate", Err: err}
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *EventSourcedStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts *HydrateOptions) error {
	switch {
	case aggregate == nil:
		return HydrateError{Err: ErrNilAggregate}
	case opts.ToVersion < 0:
		return HydrateError{AggregateID: aggregate.ID(), Err: errors.New("invalid target version")}
	case s.eventReader == nil:
		return HydrateError{AggregateID: aggregate.ID(), Err: errors.New("event store has no event stream reader")}
	}

	s.log.Debug("hydrating aggregate from event store", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	if opts == nil {
		opts = &HydrateOptions{}
	} else if err := opts.Validate(); err != nil {
		return HydrateError{AggregateID: aggregate.ID(), Err: fmt.Errorf("invalid hydrate options: %w", err)}
	}

	readOpts := eventstore.ReadStreamOptions{
		Offset:    aggregate.Version(),
		Direction: eventstore.Forward,
	}

	if opts.ToVersion > 0 {
		if v := aggregate.Version(); v == opts.ToVersion {
			s.log.Debug("aggregate already at target version, nothing to hydrate",
				"aggregate_id", aggregate.ID(),
				"version", opts.ToVersion)
			return nil
		} else if v > opts.ToVersion {
			return HydrateError{
				AggregateID: aggregate.ID(),
				Err:         fmt.Errorf("aggregate is at more recent version (%d) than requested version (%d)", v, opts.ToVersion),
			}
		}

		readOpts.Count = opts.ToVersion - aggregate.Version()
	}

	iter, err := s.eventReader.ReadStream(ctx, aggregate.ID(), readOpts)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return HydrateError{AggregateID: aggregate.ID(), Err: ErrAggregateNotFound}
	} else if err != nil {
		return HydrateError{AggregateID: aggregate.ID(), Operation: "reading event stream", Err: err}
	}

	defer iter.Close(ctx)

	// create a stream projection for the aggregate
	projector, err := projection.New(iter, projection.WithLogger(s.log.WithGroup("projection")))
	if err != nil {
		return HydrateError{AggregateID: aggregate.ID(), Operation: "creating event stream projection", Err: err}
	}

	// apply the events to the aggregate
	result, err := projector.Project(ctx, s.eventHandlerForAggregate(aggregate))
	if err != nil {
		return HydrateError{AggregateID: aggregate.ID(), Operation: "projecting event stream", Err: err}
	}

	s.log.Debug("hydrated aggregate",
		"aggregate_id", aggregate.ID(),
		"version", aggregate.Version(),
		"events_applied", result.NumProjectedEvents)

	return nil
}

// Save saves an aggregate.
func (s *EventSourcedStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	} else if s.eventWriter == nil {
		return SaveError{AggregateID: aggregate.ID(), Err: errors.New("event store has no event stream writer")}
	}

	unsavedEvents := aggregate.state.UnsavedEvents()
	if len(unsavedEvents) == 0 {
		if aggregate.Version() == 0 {
			return SaveError{AggregateID: aggregate.ID(), Err: errors.New("new aggregate has no events to save")}
		}

		s.log.Debug("no events to save")
		return nil
	}

	s.log.Debug("saving aggregate to event store", "aggregate_id", aggregate.ID(), "events", len(unsavedEvents))

	events := make([]*eventstore.WritableEvent, len(unsavedEvents))

	for i, unsavedEvent := range unsavedEvents {
		data, err := s.entityEventMarshaler.MarshalEntityEvent(unsavedEvent.EntityEvent)
		if err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "marshaling event data", Err: err}
		}

		events[i] = &eventstore.WritableEvent{
			Type: unsavedEvent.ID.TypeName(),
			Data: data,
		}
	}

	// write to event stream
	if err := s.eventWriter.AppendStream(ctx, aggregate.ID(), events, eventstore.AppendStreamOptions{
		ExpectVersion: aggregate.Version(),
	}); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving events to stream", Err: err}
	}

	// queue the events for application
	for i, unsavedEvent := range unsavedEvents {
		unsavedEvent.Version = aggregate.Version() + int64(i) + 1
		aggregate.state.WillApply(unsavedEvent)
	}

	aggregate.state.ClearUnsavedEvents()

	if opts == nil {
		opts = &SaveOptions{}
	}

	if opts.SkipApply {
		return nil
	}

	// apply the events to the aggregate
	for {
		if err := aggregate.state.ApplyNext(ctx); errors.Is(err, ErrNoUnappliedEvents) {
			return nil
		} else if err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "applying aggregate event", Err: err}
		}
	}
}

func (s *EventSourcedStore[E]) Use(eventPrototypes ...estoria.EntityEvent[E]) error {
	for _, prototype := range eventPrototypes {
		if _, registered := s.entityEventPrototypes[prototype.EventType()]; !registered {
			s.entityEventPrototypes[prototype.EventType()] = prototype.New
		} else {
			return InitializeError{
				Operation: "registering entity event prototype",
				Err:       errors.New("duplicate event type " + prototype.EventType()),
			}
		}
	}

	return nil
}

// Returns a projection.EventHandlerFunc that decodes and applies an entity event to an aggregate.
func (s *EventSourcedStore[E]) eventHandlerForAggregate(aggregate *Aggregate[E]) projection.EventHandlerFunc {
	return projection.EventHandlerFunc(func(ctx context.Context, event *eventstore.Event) error {
		if event == nil {
			return NewHydrateError(aggregate.ID(), "event handler", errors.New("received nil event in event handler"))
		}

		eventType := event.ID.TypeName()
		newEvent, ok := s.entityEventPrototypes[eventType]
		if !ok || newEvent == nil {
			return NewHydrateError(aggregate.ID(), "obtaining entity prototype",
				fmt.Errorf("no prototype registered for event type '%s'", eventType),
			)
		}

		entityEvent := newEvent()
		if entityEvent == nil {
			return NewHydrateError(aggregate.ID(), "creating entity event instance",
				fmt.Errorf("prototype.New() returned nil for event type '%s'", eventType),
			)
		}

		if err := s.entityEventMarshaler.UnmarshalEntityEvent(event.Data, entityEvent); err != nil {
			return NewHydrateError(aggregate.ID(), "unmarshaling event data",
				fmt.Errorf("failed to unmarshal event data for event type '%s': %w", eventType, err),
			)
		}

		// enqueue and apply the event immediately
		aggregate.state.WillApply(&AggregateEvent[E, estoria.EntityEvent[E]]{
			ID:          event.ID,
			Version:     event.StreamVersion,
			Timestamp:   event.Timestamp,
			EntityEvent: entityEvent,
		})
		if err := aggregate.state.ApplyNext(ctx); err != nil {
			return NewHydrateError(aggregate.ID(), "applying aggregate event",
				fmt.Errorf("failed to apply event type '%s': %w", eventType, err),
			)
		}

		return nil
	})
}

type EventSourcedStoreOption[E estoria.Entity] func(*EventSourcedStore[E]) error

func WithEventTypes[E estoria.Entity](eventPrototypes ...estoria.EntityEvent[E]) EventSourcedStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		return s.Use(eventPrototypes...)
	}
}

func WithEventStreamReader[E estoria.Entity](reader eventstore.StreamReader) EventSourcedStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.eventReader = reader
		return nil
	}
}

func WithEventStreamWriter[E estoria.Entity](writer eventstore.StreamWriter) EventSourcedStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.eventWriter = writer
		return nil
	}
}

func WithEntityEventMarshaler[E estoria.Entity](marshaler estoria.EntityEventMarshaler[E]) EventSourcedStoreOption[E] {
	return func(s *EventSourcedStore[E]) error {
		s.entityEventMarshaler = marshaler
		return nil
	}
}
