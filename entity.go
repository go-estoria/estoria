package estoria

import (
	"context"

	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// An Entity is anything whose state can be constructed by applying a series of events.
type Entity interface {
	// EntityID returns the entity's ID.
	EntityID() typeid.UUID

	// EventTypes returns a list entity event prototypes that an entity is capable of applying.
	// These are used to determine which events can be applied to an entity, as well as to create
	// new instances of those events when loading them from persistence.
	EventTypes() []EntityEvent

	// ApplyEvent applies an event to the entity, potentially changing its state.
	ApplyEvent(ctx context.Context, event EntityEvent) error
}

// EntityEvent is an event that can be applied to an entity to change its state.
type EntityEvent interface {
	EventType() string
	New() EntityEvent
}

// An EntityFactory is a function that creates a new instance of an entity.
type EntityFactory[E Entity] func(id uuid.UUID) E
