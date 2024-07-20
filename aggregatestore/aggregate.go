package aggregatestore

import (
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

// An EventSourcedAggregate is an aggregate that is managed using event sourcing.
type EventSourcedAggregate[E estoria.Entity] struct {
	// the aggregate's state (unpersisted/unapplied events)
	state estoria.AggregateState[E]
}

// Append appends events to the aggregate's unpersisted events.
func (a *EventSourcedAggregate[E]) Append(events ...*estoria.AggregateEvent) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	a.state.EnqueueForSave(events)

	return nil
}

// Entity returns the aggregate's underlying entity.
// The entity is the domain model whose state the aggregate manages.
func (a *EventSourcedAggregate[E]) Entity() E {
	return a.state.Entity()
}

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *EventSourcedAggregate[E]) ID() typeid.UUID {
	return a.state.Entity().EntityID()
}

// State returns the aggregate's underlying state, allowinig access to lower
// level operations on the aggregate's unpersisted events and unapplied events.
//
// State management is useful when implementing custom aggregate store
// functionality; it is typically not needed when using an aggregate store
// to load and save aggregates.
func (a *EventSourcedAggregate[E]) State() *estoria.AggregateState[E] {
	return &a.state
}

// Version returns the aggregate's version.
// The version is the number of events that have been applied to the aggregate.
// An aggregate with no events has a version of 0.
func (a *EventSourcedAggregate[E]) Version() int64 {
	return a.state.Version()
}
