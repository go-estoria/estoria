package main

import (
	"github.com/jefflinse/continuum"
	memoryaggregatestore "github.com/jefflinse/continuum/aggregatestore/memory"
	memoryeventstore "github.com/jefflinse/continuum/eventstore/memory"
)

type UserCreatedEvent struct {
	Username string
}

func (e UserCreatedEvent) EventType() string {
	return "user:created"
}

var _ continuum.EventData = &UserCreatedEvent{}

func main() {
	eventStore := memoryeventstore.NewEventStore()
	aggregateStore := memoryaggregatestore.NewAggregateStore(eventStore)
	aggregateStore.Save(continuum.Aggregate{
		Type: "user",
		ID:   "123",
	})

	aggregate, err := aggregateStore.Load("user", "123")
	if err != nil {
		panic(err)
	}

	aggregate.Apply(&continuum.Event{
		AggregateID: "123",
		Data:        UserCreatedEvent{Username: "jdoe"},
	})
}
