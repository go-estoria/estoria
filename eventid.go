package continuum

import "fmt"

type EventID struct {
	EventType   string
	ID          Identifier
	AggregateID AggregateID
}

func (id EventID) Equals(other EventID) bool {
	return id.EventType == other.EventType && id.ID.Equals(other.ID) && id.AggregateID.Equals(other.AggregateID)
}

func (id EventID) String() string {
	return fmt.Sprintf("%s:%s", id.EventType, id.ID)
}
