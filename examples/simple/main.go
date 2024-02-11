package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jefflinse/continuum"
	memoryes "github.com/jefflinse/continuum/eventstore/memory"
)

func main() {
	ctx := context.Background()
	configureLogging()

	// 1. Create an Event Store to store events.
	eventStore := &memoryes.EventStore{}

	// 2. Create an AggregateStore store aggregates.
	aggregateStore := &continuum.AggregateStore{
		Events: eventStore,
	}

	// 3. Define an aggregate type.
	accountAggregateType, err := continuum.NewAggregateType("account",
		func() continuum.AggregateData {
			return NewAccount()
		},
		continuum.WithAggregateIDFactory(func() continuum.Identifier {
			return continuum.UUID(uuid.New())
		}),
	)
	if err != nil {
		panic(err)
	}

	// 4. Create an aggregate instance.
	aggregate := accountAggregateType.NewAggregate(nil)

	if err := aggregate.Append(
		&UserCreatedEvent{Username: "jdoe"},
		&BalanceChangedEvent{Amount: 100},
		&UserCreatedEvent{Username: "bschmoe"},
		&BalanceChangedEvent{Amount: -14},
		&UserDeletedEvent{Username: "jdoe"},
	); err != nil {
		panic(err)
	}

	// save the aggregate
	if err := aggregateStore.Save(ctx, aggregate); err != nil {
		panic(err)
	}

	// load the aggregate
	loadedAggregate, err := aggregateStore.Load(ctx, aggregate.ID())
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

	// if err := aggregateStore.Save(ctx, aggregate); err != nil {
	// 	panic(err)
	// }

	// aggregate, err = aggregateStore.Load(ctx, continuum.StringID("123"))
	// if err != nil {
	// 	panic(err)
	// }

	// get the aggregate data
	account := loadedAggregate.Data().(*Account)
	fmt.Println(account)
}

func configureLogging() {
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
}
