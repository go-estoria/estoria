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

func (s *EventStore) AppendStream(ctx context.Context, id estoria.Identifier, events ...estoria.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stream, ok := s.Events[id.String()]
	if !ok {
		stream = make([]estoria.Event, 0)
	}

	for _, event := range events {
		for _, e := range stream {
			if e.ID() == event.ID() {
				return ErrEventExists{EventID: event.ID()}
			}
		}

		stream = append(stream, event)
	}

	s.Events[id.String()] = stream

	return nil
}

func (s *EventStore) ReadStream(ctx context.Context, aggregateID estoria.TypedID) (estoria.EventStreamIterator, error) {
	events, ok := s.Events[aggregateID.String()]
	if !ok {
		return nil, estoria.ErrStreamNotFound
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
