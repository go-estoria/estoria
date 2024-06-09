package estoria

import (
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An EventStoreEvent can be appended to and loaded from an event store.
type EventStoreEvent interface {
	ID() typeid.TypeID
	StreamID() typeid.TypeID
	StreamVersion() int64
	Timestamp() time.Time
	Data() []byte
}

// EntityEventData is the data of an event.
type EntityEventData interface {
	EventType() string
	New() EntityEventData
}
