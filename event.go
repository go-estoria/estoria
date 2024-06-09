package estoria

import (
	"time"

	"go.jetpack.io/typeid"
)

// An EventStoreEvent can be appended to and loaded from an event store.
type EventStoreEvent interface {
	ID() typeid.AnyID
	StreamID() typeid.AnyID
	StreamVersion() int64
	Timestamp() time.Time
	Data() []byte
}

// EntityEventData is the data of an event.
type EntityEventData interface {
	EventType() string
	New() EntityEventData
}
