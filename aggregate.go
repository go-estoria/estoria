package estoria

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.jetpack.io/typeid"
)

type AggregateEvent[E Entity] interface {
	ID() typeid.AnyID
	AggregateID() typeid.AnyID
	Timestamp() time.Time
	Data() EventData
}

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[E Entity] struct {
	id                  typeid.AnyID
	entity              E
	unsavedEvents       []AggregateEvent[E]
	firstUnappliedEvent *unappliedEvent
	lastUnappliedEvent  *unappliedEvent
	version             int64
}

// An AggregateBindFunc is, in Haskell terms, a function that takes an aggregate
// and a function that modifies the entity and returns a new aggregate.
type AggregateBindFunc[E Entity] func(Aggregate[E], func(E) (E, error)) (Aggregate[E], error)

// ID returns the aggregate's ID.
// The ID is the ID of the entity that the aggregate represents.
func (a *Aggregate[E]) ID() typeid.AnyID {
	return a.id
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
func (a *Aggregate[E]) Append(events ...EventData) error {
	slog.Debug("appending events to aggregate", "aggregate_id", a.ID(), "events", len(events))
	for _, eventData := range events {
		eventID, err := typeid.WithPrefix(eventData.EventType())
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

func (a *Aggregate[E]) QueueEventForApplication(event EventData) {
	if a.firstUnappliedEvent == nil {
		a.firstUnappliedEvent = &unappliedEvent{
			data: event,
		}
		a.lastUnappliedEvent = a.firstUnappliedEvent
	} else {
		a.lastUnappliedEvent.next = &unappliedEvent{
			data: event,
		}
		a.lastUnappliedEvent = a.lastUnappliedEvent.next
	}
}

func (a *Aggregate[E]) SetID(id typeid.AnyID) {
	a.id = id
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
	if a.firstUnappliedEvent == nil {
		return ErrNoUnappliedEvents
	}

	if err := a.entity.ApplyEvent(ctx, a.firstUnappliedEvent.data); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	a.firstUnappliedEvent = a.firstUnappliedEvent.next
	if a.firstUnappliedEvent == nil {
		a.lastUnappliedEvent = nil
	}

	a.version++
	return nil
}

func (a *Aggregate[E]) ApplyUnappliedEvents(ctx context.Context) error {
	for {
		err := a.ApplyNext(ctx)
		if errors.Is(err, ErrNoUnappliedEvents) {
			return ErrNoUnappliedEvents
		} else if err != nil {
			return fmt.Errorf("applying next event: %w", err)
		}
	}
}

var ErrNoUnappliedEvents = errors.New("no unapplied events")

type unsavedEvent struct {
	id          typeid.AnyID
	aggregateID typeid.AnyID
	timestamp   time.Time
	data        EventData
}

func (e *unsavedEvent) ID() typeid.AnyID {
	return e.id
}

func (e *unsavedEvent) AggregateID() typeid.AnyID {
	return e.aggregateID
}

func (e *unsavedEvent) Timestamp() time.Time {
	return e.timestamp
}

func (e *unsavedEvent) Data() EventData {
	return e.data
}

type unappliedEvent struct {
	data EventData
	next *unappliedEvent
}
