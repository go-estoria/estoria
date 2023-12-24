package main

import (
	"fmt"

	"github.com/jefflinse/continuum"
	"github.com/jefflinse/continuum/aggregatestore"
	"github.com/jefflinse/continuum/eventstore"
)

type Account struct {
	ID    string
	Users []string
}

func (a *Account) AggregateID() string {
	return a.ID
}

func (a *Account) AggregateTypeName() string {
	return "account"
}

func (a *Account) ApplyEvent(event continuum.EventData) error {
	if a == nil {
		a = &Account{}
	}

	switch e := event.(type) {
	case *UserCreatedEvent:
		a.Users = append(a.Users, e.Username)
		return nil
	default:
		return fmt.Errorf("invalid event type")
	}
}

func (a *Account) String() string {
	return fmt.Sprintf("Account %s {Users: %v}", a.ID, a.Users)
}

type UserCreatedEvent struct {
	Username string
}

func (e *UserCreatedEvent) EventTypeName() string {
	return "user:created"
}

func main() {
	eventStore := eventstore.NewMemoryEventStore()
	aggregateStore := aggregatestore.New[*Account](eventStore, func(id string) *Account {
		return &Account{
			ID:    id,
			Users: make([]string, 0),
		}
	})

	aggregate, err := aggregateStore.Create("123")
	if err != nil {
		panic(err)
	}

	if err := aggregate.Append(&UserCreatedEvent{Username: "jdoe"}); err != nil {
		panic(err)
	}

	if err := aggregate.Append(&UserCreatedEvent{Username: "bschmoe"}); err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}

	aggregate, err = aggregateStore.Load("123")
	if err != nil {
		panic(err)
	}

	fmt.Printf("%#v\n\n", aggregate)
	account := aggregate.Data
	fmt.Println(account)
}
