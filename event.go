package continuum

import "time"

// An Event is a state change to an entity.
type Event[D EventData] interface {
	AggregateID() Identifier
	AggregateType() string
	Timestamp() time.Time
	Data() D
	Version() int64
}

// BasicEvent represents a state change to an entity.
type BasicEvent struct {
	aggregateID   Identifier
	aggregateType string
	timestamp     time.Time
	data          EventData
	version       int64
}

var _ Event[EventData] = (*BasicEvent)(nil)

// NewBasicEvent creates a new BasicEvent.
func NewBasicEvent(aggregateID Identifier, aggregateType string, timestamp time.Time, data EventData, version int64) *BasicEvent {
	return &BasicEvent{
		aggregateID:   aggregateID,
		aggregateType: aggregateType,
		timestamp:     timestamp,
		data:          data,
		version:       version,
	}
}

// AggregateID returns the ID of the aggregate that the event applies to.
func (e *BasicEvent) AggregateID() Identifier {
	return e.aggregateID
}

// AggregateType returns the type of the aggregate that the event applies to.
func (e *BasicEvent) AggregateType() string {
	return e.aggregateType
}

// Timestamp returns the time that the event occurred.
func (e *BasicEvent) Timestamp() time.Time {
	return e.timestamp
}

// Data returns the event's data.
func (e *BasicEvent) Data() EventData {
	return e.data
}

// Version returns the event's version.
func (e *BasicEvent) Version() int64 {
	return e.version
}

// EventData is data associated with an event.
type EventData interface {
	EventType() string
}

// EventsByID maps aggregate IDs to events.
type EventsByID map[Identifier][]*BasicEvent

// EventsByAggregateType maps aggregate types to events by ID.
type EventsByAggregateType map[string]EventsByID
