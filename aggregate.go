package continuum

import (
	"context"
	"fmt"
	"log/slog"
)

// An Entity is anything whose state can be represented by a series of events.
// Every entity must have an ID and a type name, and must be able to apply events
// to its state.
type Entity interface {
	AggregateID() Identifier
	AggregateType() string
	ApplyEvent(ctx context.Context, event EventData) error
}

// Diffable is an entity that can be diffed against another entity to produce a
// series of events that represent the state changes between the two.
type Diffable interface {
	Diff(newer Entity) ([]EventData, error)
}

// An Aggregate is an entity that is persisted as a series of events.
type Aggregate[E Entity] struct {
	Version       int64
	Events        []*Event
	UnsavedEvents []*Event
	Entity        E
}

// Append appends the given events to the aggregate's unsaved events.
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

// Apply applies the given events to the aggregate's state.
func (a *Aggregate[E]) Apply(ctx context.Context, events ...*Event) error {
	slog.Info("applying events to aggregate", "events", len(events), "aggregate_id", a.ID())

	for _, event := range events {
		if err := a.Entity.ApplyEvent(ctx, event.Data); err != nil {
			return fmt.Errorf("applying event: %w", err)
		}

		a.Version = event.Version
	}

	return nil
}

// ID returns the aggregate's ID.
func (a *Aggregate[E]) ID() Identifier {
	return a.Entity.AggregateID()
}

// TypeName returns the aggregate's type name.
func (a *Aggregate[E]) TypeName() string {
	return a.Entity.AggregateType()
}

// AggregatesByID is a map of aggregates by ID.
type AggregatesByID[E Entity] map[string]*Aggregate[E]
