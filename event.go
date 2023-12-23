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

// EventMap maps aggregate types to aggregate IDs to slices of events.
type EventMap map[string]map[string][]*Event
