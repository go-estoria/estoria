package typeid

import (
	"github.com/gofrs/uuid/v5"
)

// An ID is a UUID with an associated type name.
type ID struct {
	Type string
	UUID uuid.UUID
}

// String returns the string representation of the ID in the format "type_uuid".
//
// Example: "user_9791012c-cd5b-4795-9c54-6085975d599b"
func (id ID) String() string {
	return id.Type + "_" + id.UUID.String()
}

// ShortString returns a shortened string representation of the ID in the format "type_xxxxxxxx",
// where "xxxxxxxx" is the first 8 characters of the UUID.
//
// Example: "user_9791012c"
//
// ShortString is provided for convenience in logging and debugging, but is not utilized by
// any core Estoria components.
func (id ID) ShortString() string {
	return id.Type + "_" + id.UUID.String()[0:8]
}

// NewV4 creates a new ID with the given type name and a new v4 UUID.
//
// UUID v4 is a randomly generated UUID.
func NewV4(typeName string) ID {
	return ID{Type: typeName, UUID: uuid.Must(uuid.NewV4())}
}

// NewV7 creates a new ID with the given type name and a new v7 UUID.
//
// UUID v7 is a time-ordered, k-sortable UUID.
func NewV7(typeName string) ID {
	return ID{Type: typeName, UUID: uuid.Must(uuid.NewV7())}
}

// New creates a new ID with the given type name and UUID.
func New(typeName string, uid uuid.UUID) ID {
	return ID{Type: typeName, UUID: uid}
}
