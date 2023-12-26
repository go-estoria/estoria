package continuum

import "time"

// EventData is the interface that all event data types must implement.
type EventData interface {
	EventType() string
}

// Event represents a state change to an entity.
type Event struct {
	AggregateID   Identifier
	AggregateType string
	Timestamp     time.Time
	Data          EventData
	Version       int64
}

// EventsByID maps aggregate IDs to events.
type EventsByID map[Identifier][]*Event

// EventsByAggregateType maps aggregate types to events by ID.
type EventsByAggregateType map[string]EventsByID
