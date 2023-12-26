package main

// UserCreatedEvent is an example event representing a user being added to an account.
type UserCreatedEvent struct {
	Username string
}

// EventType returns the event type.
func (e *UserCreatedEvent) EventType() string { return "user:created" }

// UserDeletedEvent is an example event representing a user being deleted from an account.
type UserDeletedEvent struct {
	Username string
}

// EventType returns the event type.
func (e *UserDeletedEvent) EventType() string { return "user:deleted" }

// BalanceChangedEvent is an example event representing a change in an account's balance.
type BalanceChangedEvent struct {
	Amount int
}

// EventType returns the event type.
func (e *BalanceChangedEvent) EventType() string { return "balance:changed" }
