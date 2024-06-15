package estoria

import (
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An EventStoreEvent can be appended to and loaded from an event store.
type EventStoreEvent interface {
	ID() typeid.UUID
	StreamID() typeid.TypeID
	StreamVersion() int64
	Timestamp() time.Time
	Data() []byte
}
