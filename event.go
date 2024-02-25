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
}

// The internal representation of an event.
type event struct {
	id          TypedID
	aggregateID TypedID
	timestamp   time.Time
	data        EventData
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

type EventData interface {
	EventType() string
}
