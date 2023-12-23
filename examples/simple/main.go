package main

import (
	memoryaggregatestore "github.com/jefflinse/continuum/aggregatestore/memory"
	memoryeventstore "github.com/jefflinse/continuum/eventstore/memory"
)

type Account struct {
	Users []string
}

func (a *Account) AggregateTypeName() string {
	return "account"
}

type UserCreatedEvent[A Account] struct {
	Username string
}

func (e *UserCreatedEvent[A]) EventTypeName() string {
	return "user:created"
}

func (e *UserCreatedEvent[A]) Apply(aggregate A) error {
	account := (Account)(aggregate)
	account.Users = append(account.Users, e.Username)
	return nil
}

func main() {
	eventStore := memoryeventstore.NewEventStore()
	aggregateStore := memoryaggregatestore.NewAggregateStore[*Account](eventStore)

	aggregate, err := aggregateStore.Create("123")
	if err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}

	aggregate.Apply(&UserCreatedEvent[Account]{Username: "jdoe"})

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}
}
