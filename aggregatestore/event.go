package aggregatestore

import (
	"encoding/json"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

// The internal representation of an event store event.
type event struct {
	id            typeid.UUID
	streamID      typeid.TypeID
	streamVersion int64
	timestamp     time.Time
	data          []byte
}

var _ estoria.EventStoreEvent = (*event)(nil)

// EventID returns the ID of the event.
func (e *event) ID() typeid.UUID {
	return e.id
}

// StreamID returns the ID of the stream that the event applies to.
func (e *event) StreamID() typeid.TypeID {
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

type EventDataMarshaler interface {
	Unmarshal(b []byte, d estoria.EntityEvent) error
	Marshal(d estoria.EntityEvent) ([]byte, error)
}

type JSONEventDataMarshaler struct{}

func (s JSONEventDataMarshaler) Unmarshal(b []byte, d estoria.EntityEvent) error {
	return json.Unmarshal(b, d)
}

func (s JSONEventDataMarshaler) Marshal(d estoria.EntityEvent) ([]byte, error) {
	return json.Marshal(d)
}
