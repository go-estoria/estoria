package main

import (
	"fmt"

	"github.com/jefflinse/continuum"
	"github.com/jefflinse/continuum/aggregatestore"
	memoryeventstore "github.com/jefflinse/continuum/eventstore/memory"
)

type Account struct {
	Users []string
}

func (a *Account) AggregateTypeName() string {
	return "account"
}

func (a *Account) ApplyEvent(event continuum.EventData) error {
	switch e := event.(type) {
	case *UserCreatedEvent:
		if a == nil {
			a = &Account{}
		}

		a.Users = append(a.Users, e.Username)
		return nil
	default:
		return fmt.Errorf("invalid event type")
	}
}

type UserCreatedEvent struct {
	Username string
}

func (e *UserCreatedEvent) EventTypeName() string {
	return "user:created"
}

func main() {
	eventStore := memoryeventstore.NewEventStore()
	aggregateStore := aggregatestore.New[*Account](eventStore)

	aggregate, err := aggregateStore.Create("123")
	if err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}

	if err := aggregate.Apply(&UserCreatedEvent{Username: "jdoe"}); err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}
}
