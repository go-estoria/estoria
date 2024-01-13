package continuum

import (
	"time"

	"github.com/google/uuid"
)

// An Event is a state change to an entity.
type Event interface {
	EventID() Identifier
	AggregateID() AggregateID
	Timestamp() time.Time
	Data() EventData
}

// The internal representation of an event.
type event struct {
	id          Identifier
	aggregateID AggregateID
	timestamp   time.Time
	data        EventData
}

var _ Event = (*event)(nil)

// newEvent creates a new BasicEvent.
func newEvent(aggregateID Identifier, aggregateType string, timestamp time.Time, data EventData) *event {
	return &event{
		id:          UUID(uuid.New()),
		aggregateID: AggregateID{ID: aggregateID, Type: aggregateType},
		timestamp:   timestamp,
		data:        data,
	}
}

// EventID returns the ID of the event.
func (e *event) EventID() Identifier {
	return e.id
}

// AggregateID returns the ID of the aggregate that the event applies to.
func (e *event) AggregateID() AggregateID {
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

// EventData is data associated with an event.
type EventData interface {
	EventType() string
}

// EventsByID maps aggregate IDs to events.
type EventsByID map[Identifier][]*event

// EventsByAggregateType maps aggregate types to events by ID.
type EventsByAggregateType map[string]EventsByID
