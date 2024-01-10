package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/jefflinse/continuum"
	"github.com/jefflinse/continuum/aggregatereader"
	"github.com/jefflinse/continuum/aggregatewriter"
	"github.com/jefflinse/continuum/eventreader"
	"github.com/jefflinse/continuum/eventwriter"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	ctx := context.Background()

	events := []continuum.Event{}
	eventStore := continuum.EventStore{
		Reader: eventreader.MemoryReader{Store: events},
		Writer: eventwriter.MemoryWriter{Store: events},
	}

	aggregateType := continuum.AggregateType[*Account]{
		Name: "account",
		IDFactory: func() continuum.Identifier {
			return continuum.UUID(uuid.New())
		},
		DataFactory: func() *Account {
			return NewAccount(continuum.UUID(uuid.New()))
		},
	}

	aggregateReader := aggregatereader.MemoryReader[*Account]{
		AggreateType: aggregateType,
		EventStore:   eventStore,
	}

	aggregateWritier := aggregatewriter.MemoryWriter[*Account]{
		EventStore: eventStore,
	}

	aggregateCollection, err := continuum.NewAggregateCollection(
		aggregateType,
		aggregateReader,
		aggregateWritier,
	)
	if err != nil {
		panic(err)
	}

	aggregate := aggregateCollection.Create(nil)

	if err := aggregate.Append(
		&UserCreatedEvent{Username: "jdoe"},
		&BalanceChangedEvent{Amount: 100},
		&UserCreatedEvent{Username: "bschmoe"},
		&BalanceChangedEvent{Amount: -14},
		&UserDeletedEvent{Username: "jdoe"},
	); err != nil {
		panic(err)
	}

	if err := aggregateCollection.Save(ctx, aggregate); err != nil {
		panic(err)
	}

	aggregate, err = aggregateCollection.Load(ctx, aggregate.ID.(continuum.UUID))
	if err != nil {
		panic(err)
	}

	// newEvents, err := aggregate.Data.Diff(&Account{
	// 	ID:      "123",
	// 	Users:   []string{"bschmoe", "rlowe"},
	// 	Balance: 80,
	// })
	// if err != nil {
	// 	panic(err)
	// }

	// if err := aggregate.Append(newEvents...); err != nil {
	// 	panic(err)
	// }

	// if err := aggregateCollection.Save(ctx, aggregate); err != nil {
	// 	panic(err)
	// }

	// aggregate, err = aggregateCollection.Load(ctx, continuum.StringID("123"))
	// if err != nil {
	// 	panic(err)
	// }

	account := aggregate.Data
	fmt.Println(account)
}
