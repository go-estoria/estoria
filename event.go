package continuum

import "time"

type EventData interface {
	EventType() string
}

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
