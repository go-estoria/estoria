package continuum

import "fmt"

type AggregateID struct {
	Type string
	ID   Identifier
}

func (id AggregateID) String() string {
	return fmt.Sprintf("%s:%s", id.Type, id.ID)
}

func (id AggregateID) Equals(other AggregateID) bool {
	return id.Type == other.Type && id.ID.Equals(other.ID)
}
