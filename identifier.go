package estoria

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// An Identifier is a unique identifier.
type Identifier interface {
	Equals(other Identifier) bool
	String() string
}

// StringID is a string identifier.
type StringID string

// Equals returns true if the given identifier is equal to this one.
func (id StringID) Equals(other Identifier) bool {
	otherStr, ok := other.(StringID)
	return ok && id == otherStr
}

// String returns the string representation of this identifier.
func (id StringID) String() string {
	return string(id)
}

// IntID is an int identifier.
type IntID int

// Equals returns true if the given identifier is equal to this one.
func (id IntID) Equals(other Identifier) bool {
	otherInt, ok := other.(IntID)
	return ok && id == otherInt
}

// String returns the string representation of this identifier.
func (id IntID) String() string {
	return fmt.Sprint(int(id))
}

// Int64ID is an int64 identifier.
type Int64ID int64

// Equals returns true if the given identifier is equal to this one.
func (id Int64ID) Equals(other Identifier) bool {
	otherInt, ok := other.(Int64ID)
	return ok && id == otherInt
}

// String returns the string representation of this identifier.
func (id Int64ID) String() string {
	return fmt.Sprint(int64(id))
}

// UUID is a UUID identifier.
type UUID uuid.UUID

// Equals returns true if the given identifier is equal to this one.
func (id UUID) Equals(other Identifier) bool {
	otherUUID, ok := other.(UUID)
	return ok && id == otherUUID
}

// String returns the string representation of this identifier.
func (id UUID) String() string {
	return uuid.UUID(id).String()
}
