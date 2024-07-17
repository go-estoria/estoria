package estoria

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An Aggregate is a state-managed entity.
type Aggregate[E Entity] struct {
	// the aggregate's state (unpersisted/unapplied events)
	state AggregateState[E]
}

// Append appends events to the aggregate's unpersisted events.
func (a *Aggregate[E]) Append(events ...AggregateEvent) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	a.state.unpersistedEvents = append(a.state.unpersistedEvents, events...)

	return nil
}

// Entity returns the aggregate's underlying entity.
// The entity is the domain model whose state the aggregate manages.
func (a *Aggregate[E]) Entity() E {
	return a.state.entity
}

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *Aggregate[E]) ID() typeid.UUID {
	return a.state.entity.EntityID()
}

// SetEntityAtVersion sets the aggregate's entity and version.
func (a *Aggregate[E]) SetEntityAtVersion(entity E, version int64) {
	a.state.entity = entity
	a.state.version = version
}

// State returns the aggregate's underlying state, allowinig access to lower
// level operations on the aggregate's unpersisted events and unapplied events.
func (a *Aggregate[E]) State() *AggregateState[E] {
	return &a.state
}

// Version returns the aggregate's version.
// The version is the number of events that have been applied to the aggregate.
// An aggregate with no events has a version of 0.
func (a *Aggregate[E]) Version() int64 {
	return a.state.version
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
type AggregateState[E Entity] struct {
	// the entity that the aggregate represents
	entity E

	// the number of events that have been applied to the aggregate
	version int64

	// appended to the aggregate but not yet persisted
	unpersistedEvents []AggregateEvent

	// events loaded from persistence or newly stored but not yet applied to the entity
	unappliedEvents []EntityEvent
}

// ApplyNext applies the next entity event in the apply queue to the entity.
// A successfully applied event increments the aggregate's version. If
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
// an either an entity event, which holds data specific to and event
// representinig an incremental change to the underlying entity, or
// a replacement entity, which holds the entire state of the entity
// after the event is applied.
type AggregateEvent struct {
	ID          typeid.UUID
	Version     int64
	Timestamp   time.Time
	Incremental EntityEvent
	Replacement Entity
}
