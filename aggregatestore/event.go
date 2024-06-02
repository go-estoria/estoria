package aggregatestore

import (
	"time"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

// The internal representation of an event store event.
type event struct {
	id            typeid.AnyID
	streamID      typeid.AnyID
	streamVersion int64
	timestamp     time.Time
	data          []byte
}

var _ estoria.EventStoreEvent = (*event)(nil)

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