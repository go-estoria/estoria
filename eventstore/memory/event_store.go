package memory

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

// EventStore is an in-memory event store. It should not be used in production applications.
type EventStore struct {
	events    map[string][]*eventStoreDocument
	mu        sync.RWMutex
	marshaler estoria.Marshaler[eventstore.Event, *eventstore.Event]
	outbox    *Outbox
}

// NewEventStore creates a new in-memory event store.
func NewEventStore(opts ...EventStoreOption) *EventStore {
	eventStore := &EventStore{
		events:    map[string][]*eventStoreDocument{},
		marshaler: estoria.JSONMarshaler[eventstore.Event]{},
	}

	for _, opt := range opts {
		opt(eventStore)
	}

	return eventStore
}

// AppendStream appends events to a stream.
func (s *EventStore) AppendStream(ctx context.Context, streamID typeid.UUID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stream, ok := s.events[streamID.String()]
	if !ok {
		s.events[streamID.String()] = []*eventStoreDocument{}
		stream = s.events[streamID.String()]
	}

	if opts.ExpectVersion > 0 && opts.ExpectVersion != int64(len(stream)) {
		return ErrStreamVersionMismatch{
			StreamID:        streamID,
			EventID:         events[0].ID,
			ExpectedVersion: opts.ExpectVersion,
			ActualVersion:   int64(len(stream)),
		}
	}

	preparedEvents := []*eventstore.Event{}
	tx := []*eventStoreDocument{}
	for i, writableEvent := range events {
		event := &eventstore.Event{
			ID:            writableEvent.ID,
			StreamID:      streamID,
			StreamVersion: int64(len(stream) + i + 1),
			Timestamp:     time.Now(),
			Data:          writableEvent.Data,
		}

		data, err := s.marshaler.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshaling event: %w", err)
		}

		preparedEvents = append(preparedEvents, event)
		tx = append(tx, &eventStoreDocument{
			Data: data,
		})
	}

	if s.outbox != nil {
		slog.Debug("handling events with outbox", "tx", "inherited", "events", len(tx))
		if err := s.outbox.HandleEvents(ctx, preparedEvents); err != nil {
			return fmt.Errorf("handling events: %w", err)
		}
	}

	s.events[streamID.String()] = append(stream, tx...)
	return nil
}

// ReadStream reads events from a stream.
func (s *EventStore) ReadStream(ctx context.Context, streamID typeid.UUID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
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

	return &streamIterator{
		streamID:  streamID,
		events:    stream,
		cursor:    cursor,
		direction: opts.Direction,
		limit:     limit,
		marshaler: s.marshaler,
	}, nil
}

// An EventStoreOption configures an EventStore.
type EventStoreOption func(*EventStore)

// WithEventMarshaler configures the event store to use a custom event marshaler.
func WithEventMarshaler(marshaler estoria.Marshaler[eventstore.Event, *eventstore.Event]) EventStoreOption {
	return func(s *EventStore) {
		s.marshaler = marshaler
	}
}

// WithOutbox configures the event store to use an outbox.
func WithOutbox(outbox *Outbox) EventStoreOption {
	return func(s *EventStore) {
		s.outbox = outbox
	}
}

// ErrStreamVersionMismatch is returned when the expected stream version does not match the actual stream version.
type ErrStreamVersionMismatch struct {
	StreamID        typeid.UUID
	EventID         typeid.UUID
	ExpectedVersion int64
	ActualVersion   int64
}

// Error returns the error message.
func (e ErrStreamVersionMismatch) Error() string {
	return fmt.Sprintf("stream %s version mismatch for event %s: expected %d, actual %d",
		e.StreamID,
		e.EventID,
		e.ExpectedVersion,
		e.ActualVersion)
}

func (e ErrStreamVersionMismatch) Is(err error) bool {
	_, ok := err.(ErrStreamVersionMismatch)
	return ok
}

type eventStoreDocument struct {
	Data []byte
}
