package estoria

import (
	"fmt"
	"log/slog"
	"time"

	"go.jetpack.io/typeid"
)

// An Aggregate is a reconstructed representation of an event-sourced entity's state.
type Aggregate[E Entity] struct {
	id            typeid.AnyID
	entity        E
	unsavedEvents []*unsavedEvent
	version       int64
}

// NewAggregate returns a new aggregate with the given ID and entity.
func NewAggregate[E Entity](id typeid.AnyID, entity E) *Aggregate[E] {
	return &Aggregate[E]{
		id:            id,
		entity:        entity,
		version:       0,
		unsavedEvents: nil,
	}
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
			id:        eventID,
			streamID:  a.ID(),
			timestamp: time.Now(),
			data:      eventData,
		})
	}

	return nil
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
