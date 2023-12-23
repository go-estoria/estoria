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

func (a *Account) String() string {
	return fmt.Sprintf("Account{Users: %v}", a.Users)
}

type UserCreatedEvent struct {
	Username string
}

func (e *UserCreatedEvent) EventTypeName() string {
	return "user:created"
}

func main() {
	eventStore := memoryeventstore.NewEventStore()
	aggregateStore := aggregatestore.New[*Account](eventStore, func() *Account {
		return &Account{}
	})

	aggregate, err := aggregateStore.Create("123")
	if err != nil {
		panic(err)
	}

	if err := aggregate.Append(&UserCreatedEvent{Username: "jdoe"}); err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}

	fmt.Printf("%#v\n\n", aggregate)
	account := aggregate.Data
	fmt.Println(account)
}
