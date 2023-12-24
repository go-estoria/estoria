package continuum

import "time"

type EventData interface {
	EventType() string
}

type Event struct {
	AggregateID   string
	AggregateType string
	Timestamp     time.Time
	Data          EventData
	Version       int64
}

// EventsByID maps aggregate IDs to events.
type EventsByID map[string][]*Event

// EventsByAggregateType maps aggregate types to events by ID.
type EventsByAggregateType map[string]EventsByID
