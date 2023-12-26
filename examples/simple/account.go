package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// An Account is an example entity that satifies the requirements of the continuum.Entity interface.
type Account struct {
	ID      string
	Users   []string
	Balance int
}

// NewAccount creates a new account.
func NewAccount(id continuum.Identifier) *Account {
	return &Account{
		ID:      id.String(),
		Users:   make([]string, 0),
		Balance: 0,
	}
}

// AggregateID returns the aggregate ID.
func (a *Account) AggregateID() continuum.Identifier {
	return continuum.StringID(a.ID)
}

// AggregateType returns the aggregate type.
func (a *Account) AggregateType() string {
	return "account"
}

// ApplyEvent applies an event to the entity.
func (a *Account) ApplyEvent(_ context.Context, event continuum.EventData) error {
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

// Diff diffs the entity against another entity and returns a series
// of events that represent the state changes between the two.
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
