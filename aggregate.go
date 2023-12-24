package continuum

import (
	"fmt"
	"log/slog"
)

type Entity interface {
	AggregateID() string
	AggregateTypeName() string
	ApplyEvent(event EventData) error
}

type Aggregate[E Entity] struct {
	ID            string
	Version       int64
	Events        []*Event
	UnsavedEvents []*Event
	Data          E
}

func (a *Aggregate[E]) Append(event EventData) error {
	slog.Info("appending event to aggregate", "event", event.EventTypeName(), "aggregate_id", a.ID)
	a.Version++
	a.UnsavedEvents = append(a.UnsavedEvents, &Event{
		AggregateID:   a.ID,
		AggregateType: a.TypeName(),
		Data:          event,
		Version:       a.Version,
	})

	return nil
}

func (a *Aggregate[E]) Apply(event *Event) error {
	slog.Info("applying event to aggregate", "event", event.Data.EventTypeName(), "aggregate_id", a.ID)
	if err := a.Data.ApplyEvent(event.Data); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	a.Version = event.Version

	return nil
}

func (a *Aggregate[E]) TypeName() string {
	return a.Data.AggregateTypeName()
}

type AggregatesByID[E Entity] map[string]*Aggregate[E]
