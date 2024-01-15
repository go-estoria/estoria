package continuum

import "fmt"

type AggregateID struct {
	ID  Identifier
	typ *AggregateType
}

func (id AggregateID) Equals(other AggregateID) bool {
	return id.typ.name == other.typ.name && id.ID.Equals(other.ID)
}

func (id AggregateID) String() string {
	return fmt.Sprintf("%s:%s", id.typ.name, id.ID)
}

func (id AggregateID) Type() *AggregateType {
	return id.typ
}
