package main

import (
	"fmt"

	"github.com/jefflinse/continuum"
	"github.com/jefflinse/continuum/aggregatestore"
	"github.com/jefflinse/continuum/eventstore"
)

type Account struct {
	ID      string
	Users   []string
	Balance int
}

func (a *Account) AggregateID() continuum.Identifier {
	return continuum.StringID(a.ID)
}

func (a *Account) AggregateType() string {
	return "account"
}

func (a *Account) ApplyEvent(event continuum.EventData) error {
	switch e := event.(type) {

	case *UserCreatedEvent:
		a.Users = append(a.Users, e.Username)
		return nil

	case *BalanceChangedEvent:
		a.Balance += e.Amount
		return nil

	default:
		return fmt.Errorf("invalid event type")
	}
}

func (a *Account) String() string {
	return fmt.Sprintf("Account %s {Users: %v} Balance: %d", a.ID, a.Users, a.Balance)
}

type UserCreatedEvent struct {
	Username string
}

func (e *UserCreatedEvent) EventType() string {
	return "user:created"
}

type BalanceChangedEvent struct {
	Amount int
}

func (e *BalanceChangedEvent) EventType() string {
	return "balance:changed"
}

func main() {
	eventStore := eventstore.NewMemoryEventStore()
	aggregateStore := aggregatestore.New[*Account](eventStore, func(id string) *Account {
		return &Account{
			ID:      id,
			Users:   make([]string, 0),
			Balance: 0,
		}
	})

	aggregate, err := aggregateStore.Create("123")
	if err != nil {
		panic(err)
	}

	if err := aggregate.Append(&UserCreatedEvent{Username: "jdoe"}); err != nil {
		panic(err)
	}

	if err := aggregate.Append(
		&BalanceChangedEvent{Amount: 100},
		&UserCreatedEvent{Username: "bschmoe"},
		&BalanceChangedEvent{Amount: -14},
	); err != nil {
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
