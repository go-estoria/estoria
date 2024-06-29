package estoria

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

type AggregateEvent[E Entity] interface {
	ID() typeid.UUID
	AggregateID() typeid.TypeID
	Timestamp() time.Time
	Data() EntityEvent
}

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[E Entity] struct {
	// the entity that the aggregate represents
	entity E

	// appended to the aggregate but not yet persisted
	unsavedEvents []AggregateEvent[E]

	// events loaded from persistence or newly saved but not yet applied to the entity
	unappliedEvents []EntityEvent

	// the number of events that have been applied to the aggregate
	version int64
}

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *Aggregate[E]) ID() typeid.TypeID {
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
func (a *Aggregate[E]) Append(events ...EntityEvent) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	for _, eventData := range events {
		eventID, err := typeid.NewUUID(eventData.EventType())
		if err != nil {
			return fmt.Errorf("generating event ID: %w", err)
		}

		a.unsavedEvents = append(a.unsavedEvents, &unsavedEvent{
			id:          eventID,
			aggregateID: a.ID(),
			timestamp:   time.Now(),
			data:        eventData,
		})
	}

	return nil
}

func (a *Aggregate[E]) QueueEventForApplication(event EntityEvent) {
	a.unappliedEvents = append(a.unappliedEvents, event)
}

func (a *Aggregate[E]) SetEntity(entity E) {
	a.unsavedEvents = nil
	a.entity = entity
}

func (a *Aggregate[E]) SetVersion(version int64) {
	a.version = version
}

func (a *Aggregate[E]) ClearUnsavedEvents() {
	a.unsavedEvents = nil
}

func (a *Aggregate[E]) UnsavedEvents() []AggregateEvent[E] {
	return a.unsavedEvents
}

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

var ErrNoUnappliedEvents = errors.New("no unapplied events")

type unsavedEvent struct {
	id          typeid.UUID
	aggregateID typeid.TypeID
	timestamp   time.Time
	data        EntityEvent
}

func (e *unsavedEvent) ID() typeid.UUID {
	return e.id
}

func (e *unsavedEvent) AggregateID() typeid.TypeID {
	return e.aggregateID
}

func (e *unsavedEvent) Timestamp() time.Time {
	return e.timestamp
}

func (e *unsavedEvent) Data() EntityEvent {
	return e.data
}
