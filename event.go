package continuum

import "time"

// An Event is a state change to an entity.
type Event interface {
	EventID() Identifier
	AggregateID() AggregateID
	Timestamp() time.Time
	Data() EventData
}

// BasicEvent represents a state change to an entity.
type BasicEvent struct {
	id          Identifier
	aggregateID AggregateID
	timestamp   time.Time
	data        EventData
}

var _ Event = (*BasicEvent)(nil)

// NewBasicEvent creates a new BasicEvent.
func NewBasicEvent(aggregateID Identifier, aggregateType string, timestamp time.Time, data EventData) *BasicEvent {
	return &BasicEvent{
		aggregateID: AggregateID{ID: aggregateID, Type: aggregateType},
		timestamp:   timestamp,
		data:        data,
	}
}

// EventID returns the ID of the event.
func (e *BasicEvent) EventID() Identifier {
	return e.id
}

// AggregateID returns the ID of the aggregate that the event applies to.
func (e *BasicEvent) AggregateID() AggregateID {
	return e.aggregateID
}

// Timestamp returns the time that the event occurred.
func (e *BasicEvent) Timestamp() time.Time {
	return e.timestamp
}

// Data returns the event's data.
func (e *BasicEvent) Data() EventData {
	return e.data
}

// EventData is data associated with an event.
type EventData interface {
	EventType() string
}

// EventsByID maps aggregate IDs to events.
type EventsByID map[Identifier][]*BasicEvent

// EventsByAggregateType maps aggregate types to events by ID.
type EventsByAggregateType map[string]EventsByID
