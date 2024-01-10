package continuum

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// An AggregateIDFactory is a function that returns a new aggregate ID.
type AggregateIDFactory func() Identifier

// An AggregateDataFactory is a function that returns a new aggregate data instance.
type AggregateDataFactory[D AggregateData] func() D

// An AggregateFactory is a function that returns a new aggregate instance.
type AggregateFactory[D AggregateData] func() *Aggregate[D]

type AggregateType[D AggregateData] struct {
	Name string

	// IDFactory is a function that returns a new aggregate ID.
	IDFactory AggregateIDFactory

	// DataFactory is a function that returns a new aggregate data instance.
	DataFactory AggregateDataFactory[D]
}

// AggregateFactory is a function that returns a new aggregate instance.
func (t AggregateType[D]) New(id Identifier) *Aggregate[D] {
	isNew := id == nil
	if isNew {
		id = t.IDFactory()
	}

	aggregate := &Aggregate[D]{
		Type: t,
		ID:   id,
		Data: t.DataFactory(),
	}

	slog.Info("instantiating aggregate", "new", isNew, "type", t.Name, "id", aggregate.ID)

	return aggregate
}

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
