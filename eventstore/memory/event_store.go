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
	if !ok {
		return nil, estoria.ErrStreamNotFound
	}

	return &StreamIterator{
		streamID: streamID,
		events:   stream,
		cursor:   0,
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
