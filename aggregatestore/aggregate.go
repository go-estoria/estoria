package aggregatestore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

// An Aggregate is an aggregate that is managed using event sourcing.
type Aggregate[E estoria.Entity] struct {
	// the aggregate's state (unpersisted/unapplied events)
	state AggregateState[E]
}

// Append appends events to the aggregate's unpersisted events.
func (a *Aggregate[E]) Append(events ...*estoria.AggregateEvent) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	a.state.EnqueueForPersistence(events)

	return nil
}

// Entity returns the aggregate's underlying entity.
// The entity is the domain model whose state the aggregate manages.
func (a *Aggregate[E]) Entity() E {
	return a.state.Entity()
}

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *Aggregate[E]) ID() typeid.UUID {
	return a.state.Entity().EntityID()
}

// State returns the aggregate's underlying state, allowinig access to lower
// level operations on the aggregate's unpersisted events and unapplied events.
//
// State management is useful when implementing custom aggregate store
// functionality; it is typically not needed when using an aggregate store
// to load and save aggregates.
func (a *Aggregate[E]) State() estoria.AggregateState[E] {
	return &a.state
}

// Version returns the aggregate's version.
// The version is the number of events that have been applied to the aggregate.
// An aggregate with no events has a version of 0.
func (a *Aggregate[E]) Version() int64 {
	return a.state.Version()
}

// AggregateState holds all of the aggregate's state, including the entity, version,
// unpersisted events, and unapplied events.
//
// The unpersisted events are events that have been appended to the aggregate but not yet stored.
//
// The unapplied events are events that have been loaded from persistence or newly stored
// but not yet applied to the entity.
//
// The entity is the domain object whose state the aggregate manages.
//
// The version is the number of events that have been applied to the entity.
type AggregateState[E estoria.Entity] struct {
	// the entity that the aggregate represents
	entity E

	// the number of events that have been applied to the aggregate
	version int64

	// appended to the aggregate but not yet persisted
	unpersistedEvents []*estoria.AggregateEvent

	// events loaded from persistence or newly stored but not yet applied to the entity
	unappliedEvents []estoria.EntityEvent
}

// ApplyNext applies the next entity event in the apply queue to the entity.
// A successfully applied event increments the aggregate's version. If
// there are no events in the apply queue, ErrNoUnappliedEvents is returned.
func (a *AggregateState[E]) ApplyNext(ctx context.Context) error {
	if len(a.unappliedEvents) == 0 {
		return estoria.ErrNoUnappliedEvents
	}

	if err := a.entity.ApplyEvent(ctx, a.unappliedEvents[0]); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	a.unappliedEvents = a.unappliedEvents[1:]
	a.version++

	return nil
}

// ClearUnpersistedEvents clears the aggregate's unpersisted events.
func (a *AggregateState[E]) ClearUnpersistedEvents() {
	a.unpersistedEvents = nil
}

// ClearUnappliedEvents clears the aggregate's unapplied events.
func (a *AggregateState[E]) ClearUnappliedEvents() {
	a.unappliedEvents = nil
}

// EnqueueForApplication enqueues the given entity event to be applied to
// the aggregate's entity during subsequent calls to ApplyNext.
func (a *AggregateState[E]) EnqueueForApplication(event estoria.EntityEvent) {
	a.unappliedEvents = append(a.unappliedEvents, event)
}

func (a *AggregateState[E]) EnqueueForPersistence(events []*estoria.AggregateEvent) {
	a.unpersistedEvents = append(a.unpersistedEvents, events...)
}

// Entity returns the aggregate's entity.
func (a *AggregateState[E]) Entity() E {
	return a.entity
}

// SetEntityAtVersion sets the aggregate's entity and version.
func (a *AggregateState[E]) SetEntityAtVersion(entity E, version int64) {
	a.entity = entity
	a.version = version
}

// UnappliedEvents returns the unapplied events for the aggregate.
// These are events that have been loaded from persistence or newly stored
// but not yet applied to the aggregate's entity.
func (a *AggregateState[E]) UnappliedEvents() []estoria.EntityEvent {
	return a.unappliedEvents
}

// UnpersistedEvents returns the unpersisted events for the aggregate.
// These are events that have been appended to the aggregate but not yet saved.
// They are thus not yet applied to the aggregate's entity.
func (a *AggregateState[E]) UnpersistedEvents() []*estoria.AggregateEvent {
	return a.unpersistedEvents
}

// Version returns the aggregate's version.
func (a *AggregateState[E]) Version() int64 {
	return a.version
}
