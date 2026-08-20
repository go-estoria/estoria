package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"strings"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/internal/reservedstream"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// An EventSourcedStore loads and saves aggregates using an EventStore.
// It loads and hydrates aggregates by reading events from the event store and applying
// them to the aggregate. It saves aggregates by appending events to the event store.
type EventSourcedStore[S any] struct {
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter

	aggregateType         string
	newState              estoria.StateFactory[S]
	domainEventPrototypes map[string]func() estoria.DomainEvent[S]
	domainEventCodec      estoria.DomainEventCodec[S]

	log estoria.Logger
}

var _ Store[struct{}] = (*EventSourcedStore[struct{}])(nil)

// New creates a new event sourced aggregate store.
//
// The aggregate type names the kind of aggregate the store manages and becomes the
// type component of every aggregate ID the store composes. It addresses streams in
// storage, so it must remain stable for the lifetime of the aggregates it names.
func New[S any](
	eventStore eventstore.Store,
	aggregateType string,
	stateFactory estoria.StateFactory[S],
	opts ...EventSourcedStoreOption[S],
) (*EventSourcedStore[S], error) {
	if err := typeid.ValidateTypeName(aggregateType); err != nil {
		return nil, InitializeError{Err: fmt.Errorf("invalid aggregate type: %w", err)}
	}

	if strings.HasPrefix(aggregateType, eventstore.ReservedStreamTypePrefix) && !reservedstream.Allowed(aggregateType) {
		return nil, InitializeError{Err: fmt.Errorf("aggregate type %q uses the reserved %q prefix",
			aggregateType, eventstore.ReservedStreamTypePrefix)}
	}

	store := &EventSourcedStore[S]{
		eventReader:           eventStore,
		eventWriter:           eventStore,
		aggregateType:         aggregateType,
		newState:              stateFactory,
		domainEventPrototypes: make(map[string]func() estoria.DomainEvent[S]),
		domainEventCodec:      estoria.JSONDomainEventCodec[S]{},
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

// AggregateType returns the aggregate type name used to compose the typed IDs
// under which the store's aggregates are addressed.
func (s *EventSourcedStore[S]) AggregateType() string {
	return s.aggregateType
}

// New creates a new aggregate with the given ID.
func (s *EventSourcedStore[S]) New(id uuid.UUID) *Aggregate[S] {
	return newAggregate(typeid.New(s.aggregateType, id), s.newState(id), 0)
}

// Load loads an aggregate by its ID.
func (s *EventSourcedStore[S]) Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[S], error) {
	if id == uuid.Nil {
		return nil, LoadError{Err: errors.New("aggregate ID is nil")}
	} else if opts == nil {
		opts = &LoadOptions{}
	}

	s.log.Debug("loading aggregate from event store", "aggregate_id", id)

	aggregate := s.New(id)

	if err := opts.Validate(); err != nil {
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

// Hydrate hydrates an aggregate by reading and applying events from the event store.
func (s *EventSourcedStore[S]) Hydrate(ctx context.Context, aggregate *Aggregate[S], opts *HydrateOptions) error {
	if opts == nil {
		opts = &HydrateOptions{}
	}

	switch {
	case aggregate == nil:
		return HydrateError{Err: ErrNilAggregate}
	case s.eventReader == nil:
		return HydrateError{AggregateID: aggregate.ID(), Err: errors.New("event store has no event stream reader")}
	}

	if err := opts.Validate(); err != nil {
		return HydrateError{AggregateID: aggregate.ID(), Err: fmt.Errorf("invalid hydrate options: %w", err)}
	}

	s.log.Debug("hydrating aggregate from event store", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	readOpts := eventstore.ReadStreamOptions{
		AfterVersion: aggregate.Version(),
		Direction:    eventstore.Forward,
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
		// A read filtered by AfterVersion is not a reliable signal of whether the aggregate
		// exists: it asks only for events newer than a version the aggregate has already
		// reached. Existence is decided by the aggregate's own state — one that already
		// carries state (from a snapshot, or an earlier partial hydrate) exists, so an empty
		// read past its version means there is nothing newer to apply, not that it is gone.
		// Only an unfiltered read can report absence, which is why an aggregate at version 0
		// still maps to ErrAggregateNotFound.
		//
		// This deliberately gives up one distinction. A store that follows the convention in
		// StreamReader.ReadStream returns an empty iterator for an empty filtered read, so
		// its ErrStreamNotFound here would mean the stream was genuinely deleted; that case
		// now hydrates to the stale snapshot rather than reporting not-found.
		if readOpts.AfterVersion > 0 {
			s.log.Debug("no events found after aggregate version, nothing to hydrate",
				"aggregate_id", aggregate.ID(),
				"version", aggregate.Version())
			return nil
		}

		return HydrateError{AggregateID: aggregate.ID(), Err: ErrAggregateNotFound}
	} else if err != nil {
		return HydrateError{AggregateID: aggregate.ID(), Operation: "reading event stream", Err: err}
	}

	defer iter.Close(ctx)

	// create a stream projection for the aggregate
	projector, err := projection.NewFold(iter, projection.WithLogger(s.log.WithGroup("projection")))
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

// Save saves an aggregate by appending its unsaved events to the event store.
// An error that carries ErrEventsAppended means the events were appended but
// not applied to the in-memory aggregate. A save that would grow the stream
// past the maximum representable aggregate version, or from a negative
// version, is refused before anything is appended.
func (s *EventSourcedStore[S]) Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	} else if s.eventWriter == nil {
		return SaveError{AggregateID: aggregate.ID(), Err: errors.New("event store has no event stream writer")}
	}

	unsavedEvents := aggregate.unsavedEvents
	if len(unsavedEvents) == 0 {
		if aggregate.Version() == 0 {
			return SaveError{AggregateID: aggregate.ID(), Err: errors.New("new aggregate has no events to save")}
		}

		s.log.Debug("no events to save")
		return nil
	}

	// The version guard is central here: every command path appends through a
	// save, an append past the maximum representable version would wrap the
	// version arithmetic in every consumer, and a negative version is
	// producible only by corrupt snapshot or cache state.
	if v := aggregate.Version(); v < 0 {
		return SaveError{
			AggregateID: aggregate.ID(),
			Operation:   "validating aggregate version",
			Err:         fmt.Errorf("aggregate version %d is invalid", v),
		}
	} else if int64(len(unsavedEvents)) > math.MaxInt64-v {
		return SaveError{
			AggregateID: aggregate.ID(),
			Operation:   "validating aggregate version",
			Err: fmt.Errorf("cannot append %d events at version %d: aggregate versions end at %d",
				len(unsavedEvents), v, int64(math.MaxInt64)),
		}
	}

	s.log.Debug("saving aggregate to event store", "aggregate_id", aggregate.ID(), "events", len(unsavedEvents))

	events := make([]*eventstore.WritableEvent, len(unsavedEvents))

	for i, unsavedEvent := range unsavedEvents {
		for key := range unsavedEvent.Metadata {
			if strings.HasPrefix(key, eventstore.ReservedMetadataPrefix) {
				return SaveError{
					AggregateID: aggregate.ID(),
					Operation:   "validating event metadata",
					Err:         fmt.Errorf("metadata key %q uses the reserved %q prefix", key, eventstore.ReservedMetadataPrefix),
				}
			}
		}

		data, err := s.domainEventCodec.MarshalDomainEvent(unsavedEvent.DomainEvent)
		if err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "marshaling event data", Err: err}
		}

		events[i] = &eventstore.WritableEvent{
			Type:            unsavedEvent.DomainEvent.EventType(),
			Data:            data,
			DataContentType: s.domainEventCodec.ContentType(),
			// A copy, so a backend that holds onto the map cannot alias metadata
			// the aggregate still owns while a failed save awaits its retry.
			Metadata: maps.Clone(unsavedEvent.Metadata),
		}
	}

	// write to event stream
	written, err := s.eventWriter.AppendStream(ctx, aggregate.ID(), events, eventstore.AppendStreamOptions{
		ExpectVersion: eventstore.VersionPtr(aggregate.Version()),
	})
	if err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving events to stream", Err: err}
	}

	if len(written) != len(unsavedEvents) {
		// The append succeeded, so the events are facts in the store; the store
		// only failed to report them back. Without their assigned versions nothing
		// can be applied, so this surfaces the same recovery contract as any other
		// post-append failure: discard the aggregate and reload it.
		return SaveError{
			AggregateID: aggregate.ID(),
			Operation:   "confirming appended events",
			Err: fmt.Errorf("%w: event store reported %d written events for %d appended",
				ErrEventsAppended, len(written), len(unsavedEvents)),
		}
	}

	// Queue the events for application. Identity, version, and timestamp come
	// from the store's report — the returned events are the events of record —
	// while the domain event and metadata are the ones that were appended; the
	// store returns payload bytes, and re-decoding facts already held decoded
	// would only add a failure path.
	for i, unsavedEvent := range unsavedEvents {
		aggregate.willApply(&Event[S]{
			ID:          written[i].ID,
			Version:     written[i].StreamVersion,
			Timestamp:   written[i].Timestamp,
			DomainEvent: unsavedEvent.DomainEvent,
			Metadata:    unsavedEvent.Metadata,
		})
	}

	aggregate.clearUnsavedEvents()

	if opts == nil {
		opts = &SaveOptions{}
	}

	if opts.SkipApply {
		return nil
	}

	// apply the events to the aggregate
	for {
		if err := aggregate.applyNext(); errors.Is(err, ErrNoUnappliedEvents) {
			return nil
		} else if err != nil {
			// The append above succeeded, so the events are already facts in the store.
			return SaveError{
				AggregateID: aggregate.ID(),
				Operation:   "applying aggregate event",
				Err:         fmt.Errorf("%w: %w", ErrEventsAppended, err),
			}
		}
	}
}

// Use registers domain event prototypes with the store.
//
// A prototype's New() method may return either a pointer to the event type or a
// value of the event type. For value-returning prototypes, Use inspects the
// returned type once at registration time and wraps New so that subsequent
// calls allocate an addressable pointer instance; this lets the codec
// unmarshal into the event without per-hydrate reflection.
func (s *EventSourcedStore[S]) Use(eventPrototypes ...estoria.DomainEvent[S]) error {
	const op = "registering domain event prototype"

	for _, prototype := range eventPrototypes {
		eventType := prototype.EventType()

		if err := typeid.ValidateTypeName(eventType); err != nil {
			return InitializeError{
				Operation: op,
				Err:       fmt.Errorf("invalid event type %q: %w", eventType, err),
			}
		}

		if _, registered := s.domainEventPrototypes[eventType]; registered {
			return InitializeError{
				Operation: op,
				Err:       errors.New("duplicate event type " + eventType),
			}
		}

		s.domainEventPrototypes[eventType] = pointerConstructor(prototype.New)
	}

	return nil
}

// pointerConstructor returns a constructor that always yields a DomainEvent[S]
// whose dynamic type is a pointer, so json.Unmarshal can write into it. If
// newFn already returns a pointer, it is used directly with no overhead.
// Otherwise the underlying type is captured once and each call invokes newFn
// (preserving any defaults the user set) and copies the result into a fresh
// addressable instance, returning the pointer.
func pointerConstructor[S any](newFn func() estoria.DomainEvent[S]) func() estoria.DomainEvent[S] {
	sample := newFn()
	// A nil-returning New() violates the DomainEvent contract, but the
	// hydration path already surfaces this with a clean error. Returning
	// newFn directly preserves that behavior instead of letting reflect.New(nil)
	// panic in the value-wrapping closure below.
	if sample == nil {
		return newFn
	}

	if reflect.ValueOf(sample).Kind() == reflect.Pointer {
		return newFn
	}

	t := reflect.TypeOf(sample)
	return func() estoria.DomainEvent[S] {
		ptr := reflect.New(t)
		ptr.Elem().Set(reflect.ValueOf(newFn()))
		ev, ok := ptr.Interface().(estoria.DomainEvent[S])
		if !ok {
			// Unreachable: *T satisfies DomainEvent[S] whenever T does (Go's method-set
			// rules guarantee *T's method set is a superset of T's), and T satisfies it
			// by virtue of newFn's return type.
			panic(fmt.Sprintf("pointerConstructor: *%s does not satisfy DomainEvent[S]", t.Name()))
		}
		return ev
	}
}

// Returns a projection.EventHandlerFunc that decodes and applies a domain event to an aggregate.
func (s *EventSourcedStore[S]) eventHandlerForAggregate(aggregate *Aggregate[S]) projection.EventHandlerFunc {
	return projection.EventHandlerFunc(func(_ context.Context, event *eventstore.Event) error {
		if event == nil {
			return NewHydrateError(aggregate.ID(), "event handler", errors.New("received nil event in event handler"))
		}

		eventType := event.ID.Type
		newEvent, ok := s.domainEventPrototypes[eventType]
		if !ok || newEvent == nil {
			return NewHydrateError(aggregate.ID(), "obtaining domain event prototype",
				fmt.Errorf("no prototype registered for event type '%s'", eventType),
			)
		}

		domainEvent := newEvent()
		if domainEvent == nil {
			return NewHydrateError(aggregate.ID(), "creating domain event instance",
				fmt.Errorf("prototype.New() returned nil for event type '%s'", eventType),
			)
		}

		if err := s.domainEventCodec.UnmarshalDomainEvent(event.Data, domainEvent); err != nil {
			return NewHydrateError(aggregate.ID(), "unmarshaling event data",
				fmt.Errorf("failed to unmarshal event data for event type '%s': %w", eventType, err),
			)
		}

		// enqueue and apply the event immediately
		aggregate.willApply(&Event[S]{
			ID:          event.ID,
			Version:     event.StreamVersion,
			Timestamp:   event.Timestamp,
			DomainEvent: domainEvent,
			Metadata:    event.Metadata,
		})
		if err := aggregate.applyNext(); err != nil {
			return NewHydrateError(aggregate.ID(), "applying aggregate event",
				fmt.Errorf("failed to apply event type '%s': %w", eventType, err),
			)
		}

		return nil
	})
}

// An EventSourcedStoreOption is a functional option for configuring an EventSourcedStore.
type EventSourcedStoreOption[S any] func(*EventSourcedStore[S]) error

// WithEventTypes registers domain event prototypes with the store.
func WithEventTypes[S any](eventPrototypes ...estoria.DomainEvent[S]) EventSourcedStoreOption[S] {
	return func(s *EventSourcedStore[S]) error {
		return s.Use(eventPrototypes...)
	}
}

// WithEventStreamReader sets the event stream reader for the store.
func WithEventStreamReader[S any](reader eventstore.StreamReader) EventSourcedStoreOption[S] {
	return func(s *EventSourcedStore[S]) error {
		s.eventReader = reader
		return nil
	}
}

// WithEventStreamWriter sets the event stream writer for the store.
func WithEventStreamWriter[S any](writer eventstore.StreamWriter) EventSourcedStoreOption[S] {
	return func(s *EventSourcedStore[S]) error {
		s.eventWriter = writer
		return nil
	}
}

// WithDomainEventCodec sets the domain event codec for the store.
func WithDomainEventCodec[S any](codec estoria.DomainEventCodec[S]) EventSourcedStoreOption[S] {
	return func(s *EventSourcedStore[S]) error {
		s.domainEventCodec = codec
		return nil
	}
}
