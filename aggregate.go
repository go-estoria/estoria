package continuum

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate struct {
	id            AggregateID
	data          AggregateData
	UnsavedEvents []Event
}

func (a *Aggregate) ID() AggregateID {
	return a.id
}

func (a *Aggregate) Data() AggregateData {
	return a.data
}

// Append appends the given events to the aggregate's unsaved events.
func (a *Aggregate) Append(events ...EventData) error {
	slog.Debug("appending events to aggregate", "events", len(events), "aggregate_id", a.ID)

	for _, event := range events {
		a.UnsavedEvents = append(a.UnsavedEvents, newEvent(
			a.ID(),
			time.Now(),
			event,
		))
	}

	return nil
}

// Apply applies the given events to the aggregate's state.
func (a *Aggregate) Apply(ctx context.Context, event Event) error {
	slog.Debug("applying event to aggregate", "aggregate_id", a.ID, "event", event.EventID())
	if err := a.data.ApplyEvent(ctx, event.Data()); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	return nil
}
