package continuum

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[D AggregateData] struct {
	Type          AggregateType[D]
	ID            Identifier
	Data          D
	UnsavedEvents []Event
}

// Append appends the given events to the aggregate's unsaved events.
func (a *Aggregate[D]) Append(events ...EventData) error {
	slog.Info("appending events to aggregate", "events", len(events), "aggregate_id", a.ID)

	for _, event := range events {
		a.UnsavedEvents = append(a.UnsavedEvents, NewBasicEvent(
			a.ID,
			a.Type.Name,
			time.Now(),
			event,
		))
	}

	return nil
}

// Apply applies the given events to the aggregate's state.
func (a *Aggregate[D]) Apply(ctx context.Context, event Event) error {
	slog.Info("applying event to aggregate", "event", event.EventID(), "aggregate_id", a.ID)
	if err := a.Data.ApplyEvent(ctx, event.Data()); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	return nil
}
