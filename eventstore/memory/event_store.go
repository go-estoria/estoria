package memory

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

type EventStore struct {
	events    map[string][]*estoria.EventStoreEvent
	mu        sync.RWMutex
	marshaler estoria.EventStoreEventMarshaler
	outbox    *Outbox
}

func NewEventStore(opts ...EventStoreOption) *EventStore {
	eventStore := &EventStore{
		events: map[string][]*estoria.EventStoreEvent{},
	}

	for _, opt := range opts {
		opt(eventStore)
	}

	return eventStore
}

func (s *EventStore) AppendStream(ctx context.Context, streamID typeid.UUID, opts estoria.AppendStreamOptions, events []*estoria.EventStoreEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stream, ok := s.events[streamID.String()]
	if !ok {
		s.events[streamID.String()] = []*estoria.EventStoreEvent{}
		stream = s.events[streamID.String()]
	}

	if opts.ExpectVersion > 0 && opts.ExpectVersion != int64(len(stream)) {
		return estoria.ErrStreamVersionMismatch
	}

	tx := []*estoria.EventStoreEvent{}
	for _, event := range events {
		if slices.ContainsFunc(stream, func(e *estoria.EventStoreEvent) bool {
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

func (s *EventStore) ReadStream(ctx context.Context, streamID typeid.UUID, opts estoria.ReadStreamOptions) (estoria.EventStreamIterator, error) {
	stream, ok := s.events[streamID.String()]
	if !ok || len(stream) == 0 {
		return nil, estoria.ErrStreamNotFound
	}

	cursor := int64(0)
	if opts.Direction == estoria.Reverse {
		cursor = int64(len(stream) - 1)
	}

	if opts.Offset > 0 {
		if opts.Direction == estoria.Reverse {
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

type EventStoreOption func(*EventStore)

func WithOutbox(outbox *Outbox) EventStoreOption {
	return func(s *EventStore) {
		s.outbox = outbox
	}
}

// ErrEventExists is returned when attempting to write an event that already exists.
type ErrEventExists struct {
	EventID typeid.UUID
}

// Error returns the error message.
func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
