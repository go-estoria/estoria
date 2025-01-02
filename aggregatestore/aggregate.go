package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

// An Aggregate encapsulates an entity (the aggregate root) and its state.
type Aggregate[E estoria.Entity] struct {
	// the aggregate's state (unsaved/unapplied events)
	state AggregateState[E]
}

func NewAggregate[E estoria.Entity](entity E, version int64) *Aggregate[E] {
	return &Aggregate[E]{
		state: AggregateState[E]{
			entity:  entity,
			version: version,
		},
	}
}

// Append appends events to the aggregate's unsaved events.
func (a *Aggregate[E]) Append(events ...estoria.EntityEvent[E]) error {
	estoria.GetLogger().Debug("appending events to aggregate", "aggregate_id", a.ID(), "aggregate_version", a.Version(), "events", len(events))
	for _, event := range events {
		id, err := typeid.NewUUID(event.EventType())
		if err != nil {
			return fmt.Errorf("creating event ID: %w", err)
		}

		a.state.WillSave([]*AggregateEvent[E, estoria.EntityEvent[E]]{
			{
				ID:          id,
				EntityEvent: event,
			},
		})
	}

	return nil
}

// Entity returns the aggregate's underlying entity.
// The entity is the domain model whose state the aggregate manages.
//
//nolint:ireturn // the return type is a type parameter
func (a *Aggregate[E]) Entity() E {
	return a.state.Entity()
}

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *Aggregate[E]) ID() typeid.UUID {
	return a.state.Entity().EntityID()
}

// State returns the aggregate's underlying state, allowinig access to lower
// level operations on the aggregate's unsaved events and unapplied events.
//
// State management is useful when implementing custom aggregate store
// functionality; it is typically not needed when using an aggregate store
// to load and save aggregates.
func (a *Aggregate[E]) State() *AggregateState[E] {
	return &a.state
}

// Version returns the aggregate's version.
// The version is the number of events that have been applied to the aggregate.
// An aggregate with no events has a version of 0.
func (a *Aggregate[E]) Version() int64 {
	return a.state.Version()
}

// AggregateState holds all of the aggregate's state, including the entity, version,
// unsaved events, and unapplied events.
type AggregateState[E estoria.Entity] struct {
	// The domain object whose state the aggregate manages.
	entity E

	// The number of events that have been applied to the entity.
	version int64

	// Events that have been appended to the aggregate but not yet stored.
	unsavedEvents []*AggregateEvent[E, estoria.EntityEvent[E]]

	// Events that have been loaded from persistence or newly stored but not yet applied to the entity.
	unappliedEvents []*AggregateEvent[E, estoria.EntityEvent[E]]
}

// ApplyNext applies the next entity event in the apply queue to the entity.
// A successfully applied event increments the aggregate's version. If
// there are no events in the apply queue, ErrNoUnappliedEvents is returned.
func (a *AggregateState[E]) ApplyNext(ctx context.Context) error {
	if len(a.unappliedEvents) == 0 {
		return ErrNoUnappliedEvents
	} else if a.unappliedEvents[0].Version != a.version+1 {
		return fmt.Errorf("event version mismatch: expected %d, got %d", a.version+1, a.unappliedEvents[0].Version)
	}

	entity, err := a.unappliedEvents[0].EntityEvent.ApplyTo(ctx, a.entity)
	if err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	a.entity = entity
	a.version = a.unappliedEvents[0].Version
	a.unappliedEvents = a.unappliedEvents[1:]

	return nil
}

// ClearUnsavedEvents clears the aggregate's unsaved events.
func (a *AggregateState[E]) ClearUnsavedEvents() {
	a.unsavedEvents = nil
}

// WillApply appends an aggregate event to be applied to
// the aggregate during subsequent calls to ApplyNext.
func (a *AggregateState[E]) WillApply(event *AggregateEvent[E, estoria.EntityEvent[E]]) {
	a.unappliedEvents = append(a.unappliedEvents, event)
}

// WillSave appends aggregate events to be saved on the next call to Save.
func (a *AggregateState[E]) WillSave(events []*AggregateEvent[E, estoria.EntityEvent[E]]) {
	a.unsavedEvents = append(a.unsavedEvents, events...)
}

// Entity returns the aggregate's entity.
//
//nolint:ireturn // the return type is a type parameter
func (a *AggregateState[E]) Entity() E {
	return a.entity
}

// SetEntityAtVersion sets the aggregate's entity and version.
func (a *AggregateState[E]) SetEntityAtVersion(entity E, version int64) {
	a.entity = entity
	a.version = version
}

// UnsavedEvents returns the unsaved events for the aggregate.
// These are events that have been appended to the aggregate but not yet saved.
// They are thus not yet applied to the aggregate's entity.
func (a *AggregateState[E]) UnsavedEvents() []*AggregateEvent[E, estoria.EntityEvent[E]] {
	return a.unsavedEvents
}

// Version returns the aggregate's version.
func (a *AggregateState[E]) Version() int64 {
	return a.version
}

// An AggregateEvent is an event that applies to an aggregate to change its state.
// It consists of a unique ID, a timestamp, and an entity event, which holds data specific
// to an event representinig an incremental change to the underlying entity.
type AggregateEvent[E estoria.Entity, EE estoria.EntityEvent[E]] struct {
	ID          typeid.UUID
	Version     int64
	Timestamp   time.Time
	EntityEvent EE
}

// ErrNoUnappliedEvents indicates that there are no unapplied events for the aggregate.
// This error is returned by ApplyNext when there are no events in the apply queue.
// It should be handled by the caller as a normal condition.
var ErrNoUnappliedEvents = errors.New("no unapplied events")
