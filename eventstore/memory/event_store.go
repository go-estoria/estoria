package memory

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

// EventStore is an in-memory event store. It should not be used in production applications.
type EventStore struct {
	events    map[string][]*eventstore.EventStoreEvent
	mu        sync.RWMutex
	marshaler estoria.Marshaler[eventstore.EventStoreEvent, *eventstore.EventStoreEvent]
	outbox    *Outbox
}

// NewEventStore creates a new in-memory event store.
func NewEventStore(opts ...EventStoreOption) *EventStore {
	eventStore := &EventStore{
		events:    map[string][]*eventstore.EventStoreEvent{},
		marshaler: estoria.JSONMarshaler[eventstore.EventStoreEvent]{},
	}

	for _, opt := range opts {
		opt(eventStore)
	}

	return eventStore
}

// AppendStream appends events to a stream.
func (s *EventStore) AppendStream(ctx context.Context, streamID typeid.UUID, opts eventstore.AppendStreamOptions, events []*eventstore.EventStoreEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stream, ok := s.events[streamID.String()]
	if !ok {
		s.events[streamID.String()] = []*eventstore.EventStoreEvent{}
		stream = s.events[streamID.String()]
	}

	if opts.ExpectVersion > 0 && opts.ExpectVersion != int64(len(stream)) {
		return eventstore.ErrStreamVersionMismatch
	}

	tx := []*eventstore.EventStoreEvent{}
	for _, event := range events {
		if slices.ContainsFunc(stream, func(e *eventstore.EventStoreEvent) bool {
			return event.ID.String() == e.ID.String()
		}) {
			return ErrEventExists{EventID: event.ID}
		}

		tx = append(tx, event)
	}

	if s.outbox != nil {
		slog.Debug("handling events with outbox", "tx", "inherited", "events", len(tx))
		if err := s.outbox.HandleEvents(ctx, tx); err != nil {
			return fmt.Errorf("handling events: %w", err)
		}
	}

	s.events[streamID.String()] = append(stream, tx...)
	return nil
}

// ReadStream reads events from a stream.
func (s *EventStore) ReadStream(ctx context.Context, streamID typeid.UUID, opts eventstore.ReadStreamOptions) (eventstore.EventStreamIterator, error) {
	stream, ok := s.events[streamID.String()]
	if !ok || len(stream) == 0 {
		return nil, eventstore.ErrStreamNotFound
	}

	cursor := int64(0)
	if opts.Direction == eventstore.Reverse {
		cursor = int64(len(stream) - 1)
	}

	if opts.Offset > 0 {
		if opts.Direction == eventstore.Reverse {
			cursor -= int64(opts.Offset)
		} else {
			cursor += int64(opts.Offset)
		}
	}

	limit := int64(0)
	if opts.Count > 0 {
		limit = opts.Count
	}

	return &StreamIterator{
		streamID:  streamID,
		events:    stream,
		cursor:    cursor,
		direction: opts.Direction,
		limit:     limit,
	}, nil
}

// An EventStoreOption configures an EventStore.
type EventStoreOption func(*EventStore)

// WithOutbox configures the event store to use an outbox.
func WithOutbox(outbox *Outbox) EventStoreOption {
	return func(s *EventStore) {
		s.outbox = outbox
	}
}

// ErrEventExists is returned when attempting to append an event that already exists.
type ErrEventExists struct {
	EventID typeid.UUID
}

// Error returns the error message.
func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
