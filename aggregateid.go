package continuum

import "fmt"

type AggregateID struct {
	Type *AggregateType
	ID   Identifier
}

func (id AggregateID) String() string {
	return fmt.Sprintf("%s:%s", id.Type.name, id.ID)
}

func (id AggregateID) Equals(other AggregateID) bool {
	return id.Type.name == other.Type.name && id.ID.Equals(other.ID)
}
