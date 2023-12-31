package continuum

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// An Aggregate is an entity that is persisted as a series of events.
type Aggregate[E Entity] struct {
	Version       int64
	UnsavedEvents []*BasicEvent
	Entity        E
}

// Append appends the given events to the aggregate's unsaved events.
func (a *Aggregate[E]) Append(events ...EventData) error {
	slog.Info("appending events to aggregate", "events", len(events), "aggregate_id", a.ID())

	for _, event := range events {
		a.Version++
		a.UnsavedEvents = append(a.UnsavedEvents, NewBasicEvent(
			a.ID(),
			a.TypeName(),
			time.Now(),
			event,
			a.Version,
		))
	}

	return nil
}

// Apply applies the given events to the aggregate's state.
func (a *Aggregate[E]) Apply(ctx context.Context, events ...*BasicEvent) error {
	slog.Info("applying events to aggregate", "events", len(events), "aggregate_id", a.ID())

	for _, event := range events {
		if err := a.Entity.ApplyEvent(ctx, event.Data()); err != nil {
			return fmt.Errorf("applying event: %w", err)
		}

		a.Version = event.AggregateVersion()
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
