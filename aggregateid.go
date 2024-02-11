package continuum

import "fmt"

// An AggregateID uniquely identifies an Aggregate by its type and ID.
type AggregateID struct {
	id  Identifier
	typ string
}

// Equals returns true if the other AggregateID is of the same type and has the same ID.
func (id AggregateID) Equals(other AggregateID) bool {
	return id.typ == other.typ && id.id.Equals(other.id)
}

// ID returns the ID of the AggregateID.
func (id AggregateID) ID() Identifier {
	return id.id
}

// String returns a string representation of the AggregateID
// in the form of <type>:<id>.
func (id AggregateID) String() string {
	return fmt.Sprintf("%s:%s", id.typ, id.id)
}

// Type returns the AggregateType of the AggregateID.
func (id AggregateID) Type() string {
	return id.typ
}
