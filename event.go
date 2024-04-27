package estoria

import (
	"time"

	"go.jetpack.io/typeid"
)

// An EventStoreEvent is a state change to an entity.
type EventStoreEvent interface {
	ID() typeid.AnyID
	StreamID() typeid.AnyID
	Timestamp() time.Time
	Data() []byte
}

// The internal representation of an event.
type event struct {
	id        typeid.AnyID
	streamID  typeid.AnyID
	timestamp time.Time
	data      []byte
}

var _ EventStoreEvent = (*event)(nil)

// EventID returns the ID of the event.
func (e *event) ID() typeid.AnyID {
	return e.id
}

// StreamID returns the ID of the aggregate that the event applies to.
func (e *event) StreamID() typeid.AnyID {
	return e.streamID
}

// Timestamp returns the time that the event occurred.
func (e *event) Timestamp() time.Time {
	return e.timestamp
}

// Data returns the event's data.
func (e *event) Data() []byte {
	return e.data
}

// The internal representation of an unsaved event.
type unsavedEvent struct {
	id        typeid.AnyID
	streamID  typeid.AnyID
	timestamp time.Time
	data      EventData
}

// EventData is the data of an event.
type EventData interface {
	EventType() string
	New() EventData
}
