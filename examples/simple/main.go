package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

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
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case "time":
				t := a.Value.Time()
				return slog.Attr{
					Key:   "t",
					Value: slog.StringValue(t.Format(time.TimeOnly)),
				}
			case "level":
				return slog.Attr{
					Key:   "l",
					Value: a.Value,
				}
			}

			return a
		},
	})))
	ctx := context.Background()

	events := &[]continuum.Event{}
	eventStore := continuum.EventStore{
		Reader: eventreader.MemoryReader{Store: events},
		Writer: &eventwriter.MemoryWriter{Store: events},
	}

	aggregateType := continuum.NewAggregateType[*Account]("account",
		func() *Account {
			return NewAccount()
		},
		continuum.WithAggregateIDFactory[*Account](func() continuum.Identifier {
			return continuum.UUID(uuid.New())
		}),
	)

	aggregateReader := aggregatereader.EventStoreReader[*Account]{
		AggregateType: aggregateType,
		EventStore:    eventStore,
	}

	aggregateWriter := aggregatewriter.EventStoreWriter[*Account]{
		EventStore: eventStore,
	}

	aggregateCollection, err := continuum.NewAggregateCollection(aggregateType, aggregateReader, aggregateWriter)
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

	aggregate, err = aggregateCollection.Load(ctx, aggregate.ID)
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
