package estoria

import "fmt"

// An TypedID uniquely identifies something by a type name and ID.
type TypedID struct {
	ID   Identifier
	Type string
}

// Equals returns true if the other AggregateID is of the same type and has the same ID.
func (id TypedID) Equals(other TypedID) bool {
	return id.Type == other.Type && id.ID.Equals(other.ID)
}

// String returns a string representation of the AggregateID
// in the form of <type>:<id>.
func (id TypedID) String() string {
	return fmt.Sprintf("%s:%s", id.Type, id.ID)
}
