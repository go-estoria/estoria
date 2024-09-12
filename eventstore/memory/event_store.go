package memory

import (
	"context"
	"errors"
	"fmt"
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
func NewEventStore(opts ...EventStoreOption) (*EventStore, error) {
	eventStore := &EventStore{
		events:    map[string][]*eventStoreDocument{},
		marshaler: estoria.JSONMarshaler[eventstore.Event]{},
	}

	for _, opt := range opts {
		if err := opt(eventStore); err != nil {
			return nil, eventstore.InitializationError{Err: fmt.Errorf("applying option: %w", err)}
		}
	}

	return eventStore, nil
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
		return eventstore.StreamVersionMismatchError{
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
			return eventstore.EventMarshalingError{StreamID: streamID, EventID: event.ID, Err: err}
		}

		preparedEvents = append(preparedEvents, event)

		tx = append(tx, &eventStoreDocument{
			Data: data,
		})
	}

	if s.outbox != nil {
		estoria.GetLogger().Debug("handling events with outbox", "tx", "inherited", "events", len(tx))
		s.outbox.HandleEvents(ctx, preparedEvents)
	}

	s.events[streamID.String()] = append(stream, tx...)
	return nil
}

// ReadStream reads events from a stream.
func (s *EventStore) ReadStream(_ context.Context, streamID typeid.UUID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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
			cursor -= opts.Offset
		} else {
			cursor += opts.Offset
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
type EventStoreOption func(*EventStore) error

// WithEventMarshaler configures the event store to use a custom event marshaler.
func WithEventMarshaler(marshaler estoria.Marshaler[eventstore.Event, *eventstore.Event]) EventStoreOption {
	return func(s *EventStore) error {
		if marshaler == nil {
			return errors.New("marshaler cannot be nil")
		}

		s.marshaler = marshaler
		return nil
	}
}

// WithOutbox configures the event store to use an outbox.
func WithOutbox(outbox *Outbox) EventStoreOption {
	return func(s *EventStore) error {
		if outbox == nil {
			return errors.New("outbox cannot be nil")
		}

		s.outbox = outbox
		return nil
	}
}

type eventStoreDocument struct {
	Data []byte
}
