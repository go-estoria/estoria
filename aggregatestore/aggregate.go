package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

// An Aggregate is a uniquely identifiable domain object whose state is produced by
// applying a series of events. It carries the three things that make it one: identity
// (its typed ID), continuity (its version), and state (the fold over its events).
type Aggregate[S any] struct {
	// The aggregate's typed identifier, composed by the store that created it.
	id typeid.ID

	// The number of events that have been applied to the state.
	version int64

	// The domain object whose state the aggregate manages.
	state S

	// Events that have been appended to the aggregate but not yet stored.
	unsavedEvents []*Event[S]

	// Events that have been loaded from persistence or newly stored but not yet applied to the state.
	unappliedEvents []*Event[S]
}

// newAggregate creates a new aggregate with the given ID, state, and version.
// Construction is deliberately package-internal: stores compose aggregates, and
// nothing outside this package builds one.
func newAggregate[S any](id typeid.ID, state S, version int64) *Aggregate[S] {
	return &Aggregate[S]{
		id:      id,
		version: version,
		state:   state,
	}
}

// Append appends events to the aggregate's unsaved events, to be persisted and
// applied on the next save.
func (a *Aggregate[S]) Append(events ...estoria.DomainEvent[S]) {
	estoria.GetLogger().Debug("appending events to aggregate", "aggregate_id", a.ID(), "aggregate_version", a.Version(), "events", len(events))
	for _, event := range events {
		a.unsavedEvents = append(a.unsavedEvents, &Event[S]{
			ID:          typeid.NewV4(event.EventType()),
			DomainEvent: event,
		})
	}
}

// ID returns the aggregate's typed identifier.
func (a *Aggregate[S]) ID() typeid.ID {
	return a.id
}

// State returns the aggregate's state.
// The state is the domain model whose evolution the aggregate manages.
func (a *Aggregate[S]) State() S {
	return a.state
}

// Version returns the aggregate's version.
// The version is the number of events that have been applied to the aggregate.
// An aggregate with no events has a version of 0.
func (a *Aggregate[S]) Version() int64 {
	return a.version
}

// applyNext applies the next domain event in the apply queue to the state.
// A successfully applied event increments the aggregate's version. If
// there are no events in the apply queue, ErrNoUnappliedEvents is returned.
func (a *Aggregate[S]) applyNext(ctx context.Context) error {
	if len(a.unappliedEvents) == 0 {
		return ErrNoUnappliedEvents
	} else if a.unappliedEvents[0].Version != a.version+1 {
		return fmt.Errorf("event version mismatch: expected %d, got %d", a.version+1, a.unappliedEvents[0].Version)
	}

	state, err := a.unappliedEvents[0].DomainEvent.ApplyTo(ctx, a.state)
	if err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	a.state = state
	a.version = a.unappliedEvents[0].Version
	a.unappliedEvents = a.unappliedEvents[1:]

	return nil
}

// clearUnsavedEvents clears the aggregate's unsaved events.
func (a *Aggregate[S]) clearUnsavedEvents() {
	a.unsavedEvents = nil
}

// setStateAtVersion sets the aggregate's state and version.
func (a *Aggregate[S]) setStateAtVersion(state S, version int64) {
	a.state = state
	a.version = version
}

// willApply appends an event to be applied to the aggregate during subsequent
// calls to applyNext.
func (a *Aggregate[S]) willApply(event *Event[S]) {
	a.unappliedEvents = append(a.unappliedEvents, event)
}

// An Event is an event that applies to an aggregate to change its state.
// It consists of a unique ID, a timestamp, and a domain event, which holds data
// specific to an event representing an incremental change to the underlying state.
type Event[S any] struct {
	ID          typeid.ID
	Version     int64
	Timestamp   time.Time
	DomainEvent estoria.DomainEvent[S]
}

// ErrNoUnappliedEvents indicates that there are no unapplied events for the aggregate.
// It is returned when applying events to an aggregate whose apply queue is empty and
// should be handled as a normal condition.
var ErrNoUnappliedEvents = errors.New("no unapplied events")
