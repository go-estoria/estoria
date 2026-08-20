package lifecycle

import (
	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
)

// NewStore creates the aggregate store for projection lifecycle aggregates:
// an event-sourced store wired with StreamType, the state factory, and every
// lifecycle event type. Pass the domain event store to keep lifecycle streams
// alongside domain streams (handlers filter by stream type), or a store
// backed by separate storage to keep them apart. The store uses the default
// JSON domain event codec, which StreamRouter's cutover fold and the effect
// worker depend on; it is deliberately not configurable.
func NewStore(events eventstore.Store) (aggregatestore.Store[State], error) {
	return aggregatestore.New(events, StreamType, NewState,
		aggregatestore.WithEventTypes(allDomainEvents()...))
}

// State vouches for its snapshot payloads: a snapshotting store rejects
// payloads no legitimate fold could have produced and hydrates fully from
// the events instead.
var _ aggregatestore.SnapshotStateValidator = State{}

// allDomainEvents returns a prototype of every lifecycle domain event, for
// registration with the aggregate store.
func allDomainEvents() []estoria.DomainEvent[State] {
	return []estoria.DomainEvent[State]{
		RebuildInitiated{},
		RunnerClaimed{},
		BuildStarted{},
		CaughtUp{},
		Promoted{},
		RolledBack{},
		Abandoned{},
		RetireStarted{},
		PreviousRetired{},
		RetirementPolicySet{},
	}
}
