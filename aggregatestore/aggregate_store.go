package aggregatestore

import (
	"context"
	"errors"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// A Store is a read/write store for aggregates.
// See package `aggregatestore` for implementations.
type Store[E estoria.Entity] interface {
	New(id uuid.UUID) (estoria.Aggregate[E], error)
	Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (estoria.Aggregate[E], error)
	Hydrate(ctx context.Context, aggregate estoria.Aggregate[E], opts HydrateOptions) error
	Save(ctx context.Context, aggregate estoria.Aggregate[E], opts SaveOptions) error
}

// LoadOptions are options for loading an aggregate.
type LoadOptions struct {
	// ToVersion is the version to load the aggregate to.
	//
	// Default: 0 (load to the latest version)
	ToVersion int64

	// ToTime is the time to load the aggregate to.
	//
	// Default: zero time (load to the latest version)
	// ToTime time.Time
}

// HydrateOptions are options for hydrating an aggregate.
type HydrateOptions struct {
	// ToVersion is the version to hydrate the aggregate to.
	//
	// Default: 0 (hydrate to the latest version)
	ToVersion int64

	// ToTime is the time to hydrate the aggregate to.
	//
	// Default: zero time (hydrate to the latest version)
	// ToTime time.Time
}

// SaveOptions are options for saving an aggregate.
type SaveOptions struct {
	// SkipApply skips applying the events to the entity.
	// This is useful in situations where it is desireable to delay the application of events,
	// such as when wrapping the aggregate store with additional functionality.
	//
	// Default: false
	SkipApply bool
}

// ErrAggregateNotFound is returned when an aggregate is not found.
var ErrAggregateNotFound = errors.New("aggregate not found")
