package estoria

import (
	"context"

	"go.jetpack.io/typeid"
)

type Snapshot[E Entity] interface {
	AggregateID() typeid.AnyID
	StreamVersion() int
	Entity() E
}

// Snapshotter is an interface for saving and loading snapshots of an entity.
type Snapshotter[E Entity] interface {
	LoadSnapshot(ctx context.Context, id typeid.AnyID) (Snapshot[E], error)
	SaveSnapshot(ctx context.Context, snapshot Snapshot[E]) error
}

type snapshot[E Entity] struct {
	id            typeid.AnyID
	streamVersion int
	entity        E
}

var _ Snapshot[Entity] = &snapshot[Entity]{}

func (s *snapshot[Entity]) AggregateID() typeid.AnyID {
	return s.id
}

func (s *snapshot[Entity]) StreamVersion() int {
	return s.streamVersion
}

func (s *snapshot[Entity]) Entity() Entity {
	return s.entity
}
