package main

import (
	"fmt"

	"github.com/jefflinse/continuum"
	"github.com/jefflinse/continuum/aggregatestore"
	memoryeventreader "github.com/jefflinse/continuum/eventreader/memory"
	"github.com/jefflinse/continuum/eventstore"
	memoryeventwriter "github.com/jefflinse/continuum/eventwriter/memory"
)

func main() {
	events := make(continuum.EventsByAggregateType)
	eventStore := eventstore.New(
		memoryeventreader.New(events),
		memoryeventwriter.New(events),
	)

	aggregateStore := aggregatestore.New[*Account](eventStore, NewAccount)

	aggregate, err := aggregateStore.Create(continuum.StringID("123"))
	if err != nil {
		panic(err)
	}

	if err := aggregate.Append(
		&UserCreatedEvent{Username: "jdoe"},
		&BalanceChangedEvent{Amount: 100},
		&UserCreatedEvent{Username: "bschmoe"},
		&BalanceChangedEvent{Amount: -14},
		&UserDeletedEvent{Username: "jdoe"},
	); err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}

	aggregate, err = aggregateStore.Load(continuum.StringID("123"))
	if err != nil {
		panic(err)
	}

	newEvents, err := aggregate.Data.Diff(&Account{
		ID:      "123",
		Users:   []string{"bschmoe", "rlowe"},
		Balance: 80,
	})
	if err != nil {
		panic(err)
	}

	if err := aggregate.Append(newEvents...); err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(aggregate); err != nil {
		panic(err)
	}

	aggregate, err = aggregateStore.Load(continuum.StringID("123"))
	if err != nil {
		panic(err)
	}

	account := aggregate.Data
	fmt.Println(account)
}
