package rebuild

import (
	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
)

// NewStore creates the aggregate store for rebuild aggregates: an event-sourced
// store wired with StreamType, the state factory, and every rebuild event type.
// Pass the domain event store to keep rebuild streams alongside domain streams
// (handlers filter by stream type), or a store backed by separate storage to
// keep them apart.
func NewStore(events eventstore.Store, opts ...aggregatestore.EventSourcedStoreOption[State]) (aggregatestore.Store[State], error) {
	return aggregatestore.New(events, StreamType, NewState,
		append([]aggregatestore.EventSourcedStoreOption[State]{
			aggregatestore.WithEventTypes(allDomainEvents()...),
		}, opts...)...)
}

// allDomainEvents returns a prototype of every rebuild domain event, for
// registration with the aggregate store.
func allDomainEvents() []estoria.DomainEvent[State] {
	return []estoria.DomainEvent[State]{
		Created{},
		BuildStarted{},
		BuildResumed{},
		CaughtUp{},
		Promoted{},
		RolledBack{},
		Abandoned{},
		PreviousRetired{},
	}
}
