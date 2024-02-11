package continuum

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// An Event is a state change to an entity.
type Event interface {
	ID() EventID
	AggregateID() AggregateID
	Timestamp() time.Time
	Data() EventData
}

// The internal representation of an event.
type event struct {
	id          EventID
	aggregateID AggregateID
	timestamp   time.Time
	data        any
}

var _ Event = (*event)(nil)

// newEvent creates a new BasicEvent.
func newEvent(aggregateID AggregateID, timestamp time.Time, data any) *event {
	return &event{
		id: EventID{
			EventType:   fmt.Sprintf("%T", data),
			ID:          UUID(uuid.New()),
			AggregateID: aggregateID,
		},
		aggregateID: aggregateID,
		timestamp:   timestamp,
		data:        data,
	}
}

// EventID returns the ID of the event.
func (e *event) ID() EventID {
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

type EventData interface {
}
