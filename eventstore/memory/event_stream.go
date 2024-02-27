package memory

import (
	"context"
	"io"
	"log/slog"
	"slices"

	"github.com/go-estoria/estoria"
)

type EventStream struct {
	id     estoria.Identifier
	cursor int
	events []estoria.Event
}

func (s *EventStream) Append(_ context.Context, events ...estoria.Event) error {
	tx := []estoria.Event{}
	for _, event := range events {
		if slices.ContainsFunc(s.events, func(e estoria.Event) bool {
			return event.ID().Equals(e.ID())
		}) {
			return ErrEventExists{EventID: event.ID()}
		}

		slog.Default().WithGroup("eventwriter").Debug("appending event", "event_id", event.ID())
		tx = append(tx, event)
	}

	s.events = append(s.events, events...)
	return nil
}

func (s *EventStream) Close() error {
	return nil
}

func (s *EventStream) ID() estoria.Identifier {
	return s.id
}

func (s *EventStream) Next(_ context.Context) (estoria.Event, error) {
	if s.cursor >= len(s.events) {
		return nil, io.EOF
	}

	event := s.events[s.cursor]
	s.cursor++

	return event, nil
}
