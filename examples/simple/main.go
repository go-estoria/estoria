package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jefflinse/continuum"
	"github.com/jefflinse/continuum/aggregatereader"
	"github.com/jefflinse/continuum/aggregatewriter"
	"github.com/jefflinse/continuum/eventreader"
	"github.com/jefflinse/continuum/eventwriter"
)

func main() {
	ctx := context.Background()

	events := []continuum.Event{}
	eventStore := continuum.EventStore{
		Reader: eventreader.MemoryReader{Store: events},
		Writer: eventwriter.MemoryWriter{Store: events},
	}

	aggregateType := continuum.AggregateType[*Account]{
		Name: "account",
		DataFactory: func() *Account {
			return NewAccount(continuum.UUID(uuid.New()))
		},
	}

	aggregateReader := aggregatereader.MemoryReader[*Account]{
		AggreateFactory: aggregateType.AggregateFactory,
		EventStore:      eventStore,
	}

	aggregateWritier := aggregatewriter.MemoryWriter[*Account]{EventStore: eventStore}

	aggregateStore, err := continuum.NewAggregateCollection(
		aggregateType,
		aggregateReader,
		aggregateWritier,
	)
	if err != nil {
		panic(err)
	}

	aggregate := aggregateStore.Create()

	if err := aggregate.Append(
		&UserCreatedEvent{Username: "jdoe"},
		&BalanceChangedEvent{Amount: 100},
		&UserCreatedEvent{Username: "bschmoe"},
		&BalanceChangedEvent{Amount: -14},
		&UserDeletedEvent{Username: "jdoe"},
	); err != nil {
		panic(err)
	}

	if err := aggregateStore.Save(ctx, aggregate); err != nil {
		panic(err)
	}

	aggregate, err = aggregateStore.Load(ctx, continuum.StringID("123"))
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

	if err := aggregateStore.Save(ctx, aggregate); err != nil {
		panic(err)
	}

	aggregate, err = aggregateStore.Load(ctx, continuum.StringID("123"))
	if err != nil {
		panic(err)
	}

	account := aggregate.Data
	fmt.Println(account)
}
