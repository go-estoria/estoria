package estoria

import (
	"time"

	"go.jetpack.io/typeid"
)

// An Event is a state change to an entity.
type Event interface {
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

var _ Event = (*event)(nil)

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

var _ Event = (*event)(nil)

// EventID returns the ID of the event.
func (e *unsavedEvent) ID() typeid.AnyID {
	return e.id
}

// StreamID returns the ID of the aggregate that the event applies to.
func (e *unsavedEvent) StreamID() typeid.AnyID {
	return e.streamID
}

// Timestamp returns the time that the event occurred.
func (e *unsavedEvent) Timestamp() time.Time {
	return e.timestamp
}

// Data returns the event's data.
func (e *unsavedEvent) Data() EventData {
	return e.data
}

// EventData is the data of an event.
type EventData interface {
	EventType() string
	New() EventData
}

// An EventDataSerializer serializes event data into raw event data.
type EventDataSerializer interface {
	Serialize(eventData EventData) ([]byte, error)
}

// An EventDataDeserializer deserializes raw event data into event data.
type EventDataDeserializer interface {
	Deserialize(data []byte, dest EventData) error
}
