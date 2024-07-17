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
	// the aggregate's state (unsaved/unapplied events)
	state AggregateState[E]
}

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *Aggregate[E]) ID() typeid.UUID {
	return a.state.entity.EntityID()
}

// Entity returns the aggregate's underlying entity.
// The entity is the domain model whose state the aggregate manages.
func (a *Aggregate[E]) Entity() E {
	return a.state.entity
}

// Version returns the aggregate's version.
// The version is the number of events that have been applied to the aggregate.
// An aggregate with no events has a version of 0.
func (a *Aggregate[E]) Version() int64 {
	return a.state.version
}

// Append appends the given events to the aggregate's unsaved events.
// Events are not persisted or applied to the entity until the aggregate is saved.
func (a *Aggregate[E]) Append(events ...AggregateEvent) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	a.state.unpersistedEvents = append(a.state.unpersistedEvents, events...)

	return nil
}

// ApplyNext applies the next entity event in the apply queue to the aggregate's
// entity. A successfully applied event increments the aggregate's version. If
// there are no events in the apply queue, ErrNoUnappliedEvents is returned.
func (a *AggregateState[E]) ApplyNext(ctx context.Context) error {
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

// SetEntityAtVersion sets the aggregate's entity and version.
func (a *Aggregate[E]) SetEntityAtVersion(entity E, version int64) {
	a.state.entity = entity
	a.state.version = version
}

// State returns the aggregate's underlying state, allowinig access to lower
// level operations on the aggregate's unsaved events and unapplied events.
func (a *Aggregate[E]) State() *AggregateState[E] {
	return &a.state
}

// AggregateState holds the unsaved events and unapplied events for an aggregate.
type AggregateState[E Entity] struct {
	// the entity that the aggregate represents
	entity E

	// the number of events that have been applied to the aggregate
	version int64

	// appended to the aggregate but not yet persisted
	unpersistedEvents []AggregateEvent

	// events loaded from persistence or newly saved but not yet applied to the entity
	unappliedEvents []EntityEvent
}

// EnqueueForApplication enqueues the given entity event to be applied to
// the aggregate's entity during subsequent calls to ApplyNext.
func (a *AggregateState[E]) EnqueueForApplication(event EntityEvent) {
	a.unappliedEvents = append(a.unappliedEvents, event)
}

// UnpersistedEvents returns the unpersisted events for the aggregate.
// These are events that have been appended to the aggregate but not yet saved.
// They are thus not yet applied to the aggregate's entity.
func (a *AggregateState[E]) UnpersistedEvents() []AggregateEvent {
	return a.unpersistedEvents
}

func (a *AggregateState[E]) ClearUnpersistedEvents() {
	a.unpersistedEvents = nil
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
	Version     int64
	Timestamp   time.Time
	EntityEvent EntityEvent
}
