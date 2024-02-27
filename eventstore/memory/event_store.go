package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-estoria/estoria"
)

type EventStore struct {
	Events map[string][]estoria.Event

	mu sync.RWMutex
}

func (s *EventStore) FindStream(ctx context.Context, aggregateID estoria.TypedID) (estoria.EventStream, error) {
	events, ok := s.Events[aggregateID.String()]
	if !ok {
		return nil, nil
	}

	return &EventStream{
		id:     aggregateID.ID,
		cursor: 0,
		events: events,
	}, nil
}

// ErrEventExists is returned when attempting to write an event that already exists.
type ErrEventExists struct {
	EventID estoria.TypedID
}

// Error returns the error message.
func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
