package memory

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

type EventSerde interface {
	Marshal(event estoria.EventStoreEvent) ([]byte, error)
	Unmarshal(data []byte, dest estoria.EventStoreEvent) error
}

type EventStore struct {
	events map[string][]estoria.EventStoreEvent
	mu     sync.RWMutex
	serde  EventSerde
}

func NewEventStore() *EventStore {
	return &EventStore{
		events: map[string][]estoria.EventStoreEvent{},
	}
}

func (s *EventStore) AppendStream(ctx context.Context, streamID typeid.TypeID, opts estoria.AppendStreamOptions, events []estoria.EventStoreEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stream := s.events[streamID.String()]
	tx := []estoria.EventStoreEvent{}
	for _, event := range events {
		if slices.ContainsFunc(stream, func(e estoria.EventStoreEvent) bool {
			return event.ID().String() == e.ID().String()
		}) {
			return ErrEventExists{EventID: event.ID()}
		}

		tx = append(tx, event)
	}

	s.events[streamID.String()] = append(stream, tx...)
	return nil
}

func (s *EventStore) ReadStream(ctx context.Context, streamID typeid.TypeID, opts estoria.ReadStreamOptions) (estoria.EventStreamIterator, error) {
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

// ErrEventExists is returned when attempting to write an event that already exists.
type ErrEventExists struct {
	EventID typeid.TypeID
}

// Error returns the error message.
func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
