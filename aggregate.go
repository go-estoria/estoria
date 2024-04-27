package estoria

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.jetpack.io/typeid"
)

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[E Entity] struct {
	id                  typeid.AnyID
	entity              E
	unsavedEvents       []*unsavedEvent
	firstUnappliedEvent *unappliedEvent
	lastUnappliedEvent  *unappliedEvent
	version             int64
}

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

func (a *Aggregate[E]) applyUnappliedEvents(ctx context.Context) error {
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

type unappliedEvent struct {
	data EventData
	next *unappliedEvent
}
