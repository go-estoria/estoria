package main

import "github.com/go-estoria/estoria"

// UserCreatedEvent is an example event representing a user being added to an account.
type UserCreatedEvent struct {
	Username string
}

// EventType returns the event type.
func (e *UserCreatedEvent) EventType() string { return "user:created" }

// New constructs a new instance of the event data.
func (e *UserCreatedEvent) New() estoria.EventData {
	return &UserCreatedEvent{}
}

// UserDeletedEvent is an example event representing a user being deleted from an account.
type UserDeletedEvent struct {
	Username string
}

// EventType returns the event type.
func (e *UserDeletedEvent) EventType() string { return "user:deleted" }

// New constructs a new instance of the event data.
func (e *UserDeletedEvent) New() estoria.EventData {
	return &UserDeletedEvent{}
}

// BalanceChangedEvent is an example event representing a change in an account's balance.
type BalanceChangedEvent struct {
	Amount int
}

// EventType returns the event type.
func (e *BalanceChangedEvent) EventType() string { return "balance:changed" }

// New constructs a new instance of the event data.
func (e *BalanceChangedEvent) New() estoria.EventData {
	return &BalanceChangedEvent{}
}
