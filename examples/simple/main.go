package main

import (
	"fmt"
	"log/slog"

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

	case *BalanceChangedEvent:
		slog.Info("applying balance changed event", "amount", e.Amount)
		a.Balance += e.Amount
		return nil

	case *UserCreatedEvent:
		slog.Info("applying user created event", "username", e.Username)
		a.Users = append(a.Users, e.Username)
		return nil

	case *UserDeletedEvent:
		slog.Info("applying user deleted event", "username", e.Username)
		for i, user := range a.Users {
			if user == e.Username {
				a.Users = append(a.Users[:i], a.Users[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("user %s not found", e.Username)

	default:
		return fmt.Errorf("invalid event type")
	}
}

func (a *Account) Diff(newer continuum.Entity) ([]continuum.EventData, error) {
	slog.Info("diffing account", "account", a, "newer", newer)
	newerAccount, ok := newer.(*Account)
	if !ok {
		return nil, fmt.Errorf("invalid entity type")
	}

	events := make([]continuum.EventData, 0)

	// map of user: newly-added
	userMap := make(map[string]bool)
	for _, user := range a.Users {
		userMap[user] = false
	}

	for _, user := range newerAccount.Users {
		if _, exists := userMap[user]; exists {
			userMap[user] = true
		} else {
			events = append(events, &UserCreatedEvent{
				Username: user,
			})
		}
	}

	for user, existsInNewer := range userMap {
		if !existsInNewer {
			events = append(events, &UserDeletedEvent{
				Username: user,
			})
		}
	}

	// balance difference
	if a.Balance != newerAccount.Balance {
		events = append(events, &BalanceChangedEvent{
			Amount: newerAccount.Balance - a.Balance,
		})
	}

	slog.Info("diffed accounts", "events", len(events))
	return events, nil
}

func (a *Account) String() string {
	return fmt.Sprintf("Account %s {Users: %v} Balance: %d", a.ID, a.Users, a.Balance)
}

type UserCreatedEvent struct {
	Username string
}

func (e *UserCreatedEvent) EventType() string { return "user:created" }

type UserDeletedEvent struct {
	Username string
}

func (e *UserDeletedEvent) EventType() string { return "user:deleted" }

type BalanceChangedEvent struct {
	Amount int
}

func (e *BalanceChangedEvent) EventType() string { return "balance:changed" }

func main() {
	eventStore := eventstore.New()
	aggregateStore := aggregatestore.New[*Account](eventStore, func(id continuum.Identifier) *Account {
		return &Account{
			ID:      id.String(),
			Users:   make([]string, 0),
			Balance: 0,
		}
	})

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

	events, err := aggregate.Data.Diff(&Account{
		ID:      "123",
		Users:   []string{"bschmoe", "rlowe"},
		Balance: 80,
	})
	if err != nil {
		panic(err)
	}

	if err := aggregate.Append(events...); err != nil {
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
