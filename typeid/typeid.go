package typeid

import (
	"github.com/gofrs/uuid/v5"
)

// An ID is a UUID with an associated type name.
type ID struct {
	Type string
	ID   uuid.UUID
}

func (id ID) String() string {
	return id.Type + "_" + id.ID.String()
}

func (id ID) ShortString() string {
	if len(id.ID.String()) < 8 {
		return id.String()
	}

	return id.Type + "_" + id.ID.String()[0:8]
}

// NewV4 creates a new ID with the given type name and a new v4 UUID.
//
// UUID v4 is a randomly generated UUID.
func NewV4(typeName string) ID {
	return ID{Type: typeName, ID: uuid.Must(uuid.NewV4())}
}

// NewV7 creates a new ID with the given type name and a new v7 UUID.
//
// UUID v7 is a time-ordered, k-sortable UUID.
func NewV7(typeName string) ID {
	return ID{Type: typeName, ID: uuid.Must(uuid.NewV7())}
}

// New creates a new ID with the given type name and UUID.
func New(typeName string, uid uuid.UUID) ID {
	return ID{Type: typeName, ID: uid}
}
