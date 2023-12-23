package continuum

import "time"

type Event struct {
	AggregateID string
	Time        time.Time
	Version     int64
	Metadata    map[string]any
	Data        EventData
}

type EventData interface {
	Type() string
	Version() int64
}
