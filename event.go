package continuum

import "time"

// An Event is a state change to an entity.
type Event[D EventData] interface {
	AggregateID() Identifier
	AggregateType() string
	AggregateVersion() int64
	Timestamp() time.Time
	Data() D
}

// BasicEvent represents a state change to an entity.
type BasicEvent struct {
	aggregateID      Identifier
	aggregateType    string
	aggregateVersion int64
	timestamp        time.Time
	data             EventData
}

var _ Event[EventData] = (*BasicEvent)(nil)

// NewBasicEvent creates a new BasicEvent.
func NewBasicEvent(aggregateID Identifier, aggregateType string, timestamp time.Time, data EventData, aggregateVersion int64) *BasicEvent {
	return &BasicEvent{
		aggregateID:      aggregateID,
		aggregateType:    aggregateType,
		aggregateVersion: aggregateVersion,
		timestamp:        timestamp,
		data:             data,
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

// AggregateVersion returns the version of the aggregate that the event represents.
func (e *BasicEvent) AggregateVersion() int64 {
	return e.aggregateVersion
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
