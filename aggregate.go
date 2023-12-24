package continuum

import (
	"fmt"
	"log/slog"
)

type Entity interface {
	AggregateID() string
	AggregateType() string
	ApplyEvent(event EventData) error
}

type Aggregate[E Entity] struct {
	Version       int64
	Events        []*Event
	UnsavedEvents []*Event
	Data          E
}

func (a *Aggregate[E]) Append(events ...EventData) error {
	slog.Info("appending events to aggregate", "events", len(events), "aggregate_id", a.ID())

	for _, event := range events {
		a.Version++
		a.UnsavedEvents = append(a.UnsavedEvents, &Event{
			AggregateID:   a.ID(),
			AggregateType: a.TypeName(),
			Data:          event,
			Version:       a.Version,
		})
	}

	return nil
}

func (a *Aggregate[E]) Apply(events ...*Event) error {
	slog.Info("applying events to aggregate", "events", len(events), "aggregate_id", a.ID())

	for _, event := range events {
		if err := a.Data.ApplyEvent(event.Data); err != nil {
			return fmt.Errorf("applying event: %w", err)
		}

		a.Version = event.Version
	}

	return nil
}

func (a *Aggregate[E]) ID() string {
	return a.Data.AggregateID()
}

func (a *Aggregate[E]) TypeName() string {
	return a.Data.AggregateType()
}

type AggregatesByID[E Entity] map[string]*Aggregate[E]
