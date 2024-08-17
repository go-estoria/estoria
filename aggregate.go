package estoria

import (
	"context"
	"errors"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An Aggregate is a state-managed entity.
type Aggregate[E Entity] interface {
	Append(events ...*AggregateEvent) error
	Entity() E
	ID() typeid.UUID
	State() AggregateState[E]
	Version() int64
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
type AggregateState[E Entity] interface {
	ApplyNext(ctx context.Context) error
	ClearUnappliedEvents()
	ClearUnpersistedEvents()
	EnqueueForApplication(event *AggregateEvent)
	EnqueueForPersistence(events []*AggregateEvent)
	SetEntityAtVersion(entity E, version int64)
	UnappliedEvents() []*AggregateEvent
	UnpersistedEvents() []*AggregateEvent
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
	EntityEvent EntityEvent
}
