package estoria

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.jetpack.io/typeid"
)

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[E Entity] struct {
	id            typeid.AnyID
	data          E
	unsavedEvents []*event
}

func (a *Aggregate[E]) ID() typeid.AnyID {
	return a.id
}

func (a *Aggregate[E]) Entity() E {
	return a.data
}

// Append appends the given events to the aggregate's unsaved events.
func (a *Aggregate[E]) Append(events ...EventData) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	for _, eventData := range events {
		eventID, err := typeid.WithPrefix(eventData.EventType())
		if err != nil {
			return fmt.Errorf("generating event ID: %w", err)
		}

		a.unsavedEvents = append(a.unsavedEvents, &event{
			id:        eventID,
			streamID:  a.ID(),
			timestamp: time.Now(),
			data:      eventData,
		})
	}

	return nil
}

// Apply applies the given events to the aggregate's state.
func (a *Aggregate[E]) apply(ctx context.Context, evt *event) error {
	slog.Debug("applying event to aggregate", "aggregate_id", a.ID(), "event_id", evt.ID())
	if err := a.data.ApplyEvent(ctx, evt.data); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	return nil
}
