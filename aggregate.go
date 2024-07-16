package estoria

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[E Entity] struct {
	// the entity that the aggregate represents
	entity E

	// appended to the aggregate but not yet persisted
	unsavedEvents []AggregateEvent

	// events loaded from persistence or newly saved but not yet applied to the entity
	unappliedEvents []EntityEvent

	// the number of events that have been applied to the aggregate
	version int64
}

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *Aggregate[E]) ID() typeid.UUID {
	return a.entity.EntityID()
}

// Entity returns the aggregate's underlying entity.
// The entity is the object that the aggregate represents.
func (a *Aggregate[E]) Entity() E {
	return a.entity
}

// Version returns the aggregate's version.
// The version is the number of events that have been applied to the aggregate.
// An aggregate with no events has a version of 0.
func (a *Aggregate[E]) Version() int64 {
	return a.version
}

// Append appends the given events to the aggregate's unsaved events.
// Events are not persisted or applied to the entity until the aggregate is saved.
func (a *Aggregate[E]) Append(events ...AggregateEvent) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	a.unsavedEvents = append(a.unsavedEvents, events...)

	return nil
}

// AddForApply enqueues the given entity event to be applied to the
// aggregate's entity during subsequent calls to ApplyNext.
func (a *Aggregate[E]) AddForApply(event EntityEvent) {
	a.unappliedEvents = append(a.unappliedEvents, event)
}

// SetEntity sets the aggregate's entity, replacing the current entity.
func (a *Aggregate[E]) SetEntity(entity E) {
	a.unsavedEvents = nil
	a.entity = entity
}

// SetVersion sets the aggregate's version, replacing the current version.
func (a *Aggregate[E]) SetVersion(version int64) {
	a.version = version
}

// ClearUnsavedEvents clears the aggregate's unsaved events.
func (a *Aggregate[E]) ClearUnsavedEvents() {
	a.unsavedEvents = nil
}

// UnsavedEvents returns the aggregate's unsaved events.
func (a *Aggregate[E]) UnsavedEvents() []AggregateEvent {
	return a.unsavedEvents
}

// ApplyNext applies the next entity event in the apply queue to the aggregate's
// entity. A successfully applied event increments the aggregate's version. If
// there are no events in the apply queue, ErrNoUnappliedEvents is returned.
func (a *Aggregate[E]) ApplyNext(ctx context.Context) error {
	if len(a.unappliedEvents) == 0 {
		return ErrNoUnappliedEvents
	}

	if err := a.entity.ApplyEvent(ctx, a.unappliedEvents[0]); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	a.unappliedEvents = a.unappliedEvents[1:]
	a.version++

	return nil
}

// ErrNoUnappliedEvents indicates that there are no unapplied events for the aggregate.
// This error is returned by ApplyNext when there are no events in the apply queue.
// It should be handled by the caller as a normal condition.
var ErrNoUnappliedEvents = errors.New("no unapplied events")

// An AggregateEvent is an event that that applies to an aggregate
// to change its state. It consists of a unique ID, a timestamp, and
// an entity event, which holds data specific to the event.
type AggregateEvent struct {
	ID          typeid.UUID
	Timestamp   time.Time
	EntityEvent EntityEvent
}
