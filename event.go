package continuum

import "time"

type EventData interface {
	EventTypeName() string
}

type Event struct {
	AggregateType string
	AggregateID   string
	Time          time.Time
	Data          EventData
	Version       int64
}

// EventsByID maps aggregate IDs to events.
type EventsByID map[string][]*Event

// EventsByAggregateType maps aggregate types to events by ID.
type EventsByAggregateType map[string]EventsByID
