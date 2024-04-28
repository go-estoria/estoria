package estoria

import (
	"time"

	"go.jetpack.io/typeid"
)

// An EventStoreEvent can be appended to and loaded from an event store.
type EventStoreEvent interface {
	ID() typeid.AnyID
	StreamID() typeid.AnyID
	StreamVersion() int64
	Timestamp() time.Time
	Data() []byte
}

// The internal representation of an event store event.
type event struct {
	id            typeid.AnyID
	streamID      typeid.AnyID
	streamVersion int64
	timestamp     time.Time
	data          []byte
}

var _ EventStoreEvent = (*event)(nil)

// EventID returns the ID of the event.
func (e *event) ID() typeid.AnyID {
	return e.id
}

// StreamID returns the ID of the stream that the event applies to.
func (e *event) StreamID() typeid.AnyID {
	return e.streamID
}

// StreamVersion returns the version of the stream that the event represents.
func (e *event) StreamVersion() int64 {
	return e.streamVersion
}

// Timestamp returns the time that the event occurred.
func (e *event) Timestamp() time.Time {
	return e.timestamp
}

// Data returns the event's data.
func (e *event) Data() []byte {
	return e.data
}

type AggregateEvent interface {
	ID() typeid.AnyID
	AggregateID() typeid.AnyID
	Timestamp() time.Time
	Data() EventData
}

// The internal representation of an unsaved event.
type unsavedEvent struct {
	id          typeid.AnyID
	aggregateID typeid.AnyID
	timestamp   time.Time
	data        EventData
}

var _ AggregateEvent = (*unsavedEvent)(nil)

// ID returns the ID of the event.
func (e *unsavedEvent) ID() typeid.AnyID {
	return e.id
}

// AggregateID returns the ID of the aggregate that the event applies to.
func (e *unsavedEvent) AggregateID() typeid.AnyID {
	return e.aggregateID
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
