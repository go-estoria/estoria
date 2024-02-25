package estoria

import (
	"time"

	"github.com/google/uuid"
)

// An Event is a state change to an entity.
type Event interface {
	ID() TypedID
	AggregateID() TypedID
	Timestamp() time.Time
	Data() EventData
	RawData() []byte
}

// The internal representation of an event.
type event struct {
	id          TypedID
	aggregateID TypedID
	timestamp   time.Time
	data        EventData
	raw         []byte
}

var _ Event = (*event)(nil)

// newEvent creates a new BasicEvent.
func newEvent(aggregateID TypedID, timestamp time.Time, data EventData) *event {
	return &event{
		id: TypedID{
			Type: data.EventType(),
			ID:   UUID(uuid.New()),
		},
		aggregateID: aggregateID,
		timestamp:   timestamp,
		data:        data,
	}
}

// EventID returns the ID of the event.
func (e *event) ID() TypedID {
	return e.id
}

// AggregateID returns the ID of the aggregate that the event applies to.
func (e *event) AggregateID() TypedID {
	return e.aggregateID
}

// Timestamp returns the time that the event occurred.
func (e *event) Timestamp() time.Time {
	return e.timestamp
}

// Data returns the event's data.
func (e *event) Data() EventData {
	return e.data
}

// RawData returns the event's raw data.
func (e *event) RawData() []byte {
	return e.raw
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
