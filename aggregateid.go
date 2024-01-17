package continuum

import "fmt"

// An AggregateID uniquely identifies an Aggregate by its type and ID.
type AggregateID struct {
	id  Identifier
	typ *AggregateType
}

// Equals returns true if the other AggregateID is of the same type and has the same ID.
func (id AggregateID) Equals(other AggregateID) bool {
	return id.typ.name == other.typ.name && id.id.Equals(other.id)
}

// ID returns the ID of the AggregateID.
func (id AggregateID) ID() Identifier {
	return id.id
}

// NewAggregate creates a new Aggregate with the ID of the AggregateID.
func (id AggregateID) NewAggregate() *Aggregate {
	return id.Type().NewAggregate(id.id)
}

// String returns a string representation of the AggregateID
// in the form of <type>:<id>.
func (id AggregateID) String() string {
	return fmt.Sprintf("%s:%s", id.typ.name, id.id)
}

// Type returns the AggregateType of the AggregateID.
func (id AggregateID) Type() *AggregateType {
	return id.typ
}
