package estoria

import "fmt"

// An AggregateID uniquely identifies an Aggregate by its type and ID.
type AggregateID struct {
	ID   Identifier
	Type string
}

// Equals returns true if the other AggregateID is of the same type and has the same ID.
func (id AggregateID) Equals(other AggregateID) bool {
	return id.Type == other.Type && id.ID.Equals(other.ID)
}

// String returns a string representation of the AggregateID
// in the form of <type>:<id>.
func (id AggregateID) String() string {
	return fmt.Sprintf("%s:%s", id.Type, id.ID)
}
