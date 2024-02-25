package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/go-estoria/estoria"

	// memoryes "github.com/go-estoria/estoria/eventstore/memory"
	mongoes "github.com/go-estoria/estoria-contrib/mongodb/eventstore"
)

func main() {
	ctx := context.Background()
	configureLogging()

	// 1. Create an Event Store to store events.
	var eventStore estoria.EventStore

	// MongoDB Event Store
	{
		// Prereq: Create a MongoDB client.
		mongoClient, err := mongoes.NewDefaultMongoDBClient(ctx, "example-app", os.Getenv("MONGODB_URI"))
		if err != nil {
			panic(err)
		}
		defer mongoClient.Disconnect(ctx)

		slog.Info("pinging MongoDB", "uri", os.Getenv("MONGODB_URI"))
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := mongoClient.Ping(pingCtx, nil); err != nil {
			log.Fatalf("failed to ping MongoDB: %v", err)
		}

		eventStore, err = mongoes.NewEventStore(mongoClient,
			mongoes.WithDatabaseName("example-app"),
			mongoes.WithEventsCollectionName("events"),
			mongoes.WithLogger(slog.Default()),
		)
		if err != nil {
			panic(err)
		}
	}

	// Memory Event Store
	// {
	// eventStore = &memoryes.EventStore{
	// 	Events: []estoria.Event{},
	// }
	// }

	// 2. Create an AggregateStore store aggregates.
	aggregateStore := estoria.NewAggregateStore(eventStore, NewAccount)
	aggregateStore.Allow(func() estoria.EventData { return &UserCreatedEvent{} })
	aggregateStore.Allow(func() estoria.EventData { return &UserDeletedEvent{} })
	aggregateStore.Allow(func() estoria.EventData { return &BalanceChangedEvent{} })

	// 4. Create an aggregate instance.
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

	// aggregate, err = aggregateStore.Load(ctx, estoria.StringID("123"))
	// if err != nil {
	// 	panic(err)
	// }

	// get the aggregate data
	account := loadedAggregate.Entity()
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
