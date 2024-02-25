package estoria

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[E Entity] struct {
	id            TypedID
	data          E
	UnsavedEvents []Event
}

func (a *Aggregate[E]) ID() TypedID {
	return a.id
}

func (a *Aggregate[E]) Entity() E {
	return a.data
}

// Append appends the given events to the aggregate's unsaved events.
func (a *Aggregate[E]) Append(events ...EventData) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	for _, eventData := range events {
		a.UnsavedEvents = append(a.UnsavedEvents, newEvent(
			a.ID(),
			time.Now(),
			eventData,
		))
	}

	return nil
}

// Apply applies the given events to the aggregate's state.
func (a *Aggregate[E]) Apply(ctx context.Context, event Event) error {
	slog.Debug("applying event to aggregate", "aggregate_id", a.ID(), "event_id", event.ID())
	if err := a.data.ApplyEvent(ctx, event.Data()); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	return nil
}
