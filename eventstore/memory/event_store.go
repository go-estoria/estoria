package memory

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type EventStore struct {
	Events map[string][]estoria.Event

	mu sync.RWMutex
}

func (s *EventStore) AppendStream(ctx context.Context, streamID typeid.AnyID, opts estoria.AppendStreamOptions, events ...estoria.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stream := s.Events[streamID.String()]
	tx := []estoria.Event{}
	for _, event := range events {
		if slices.ContainsFunc(stream, func(e estoria.Event) bool {
			return event.ID().String() == e.ID().String()
		}) {
			return ErrEventExists{EventID: event.ID()}
		}

		tx = append(tx, event)
	}

	s.Events[streamID.String()] = append(stream, events...)
	return nil
}

func (s *EventStore) ReadStream(ctx context.Context, streamID typeid.AnyID, opts estoria.ReadStreamOptions) (estoria.EventStreamIterator, error) {
	stream, ok := s.Events[streamID.String()]
	if !ok || len(stream) == 0 {
		return nil, estoria.ErrStreamNotFound
	}

	cursor := int64(0)
	if opts.Offset > 0 {
		cursor = opts.Offset
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
	EventID typeid.AnyID
}

// Error returns the error message.
func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
