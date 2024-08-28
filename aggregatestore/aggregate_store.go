package aggregatestore

import (
	"context"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// A Store is a read/write store for aggregates.
// See package `aggregatestore` for implementations.
type Store[E estoria.Entity] interface {
	New(id uuid.UUID) (*Aggregate[E], error)
	Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*Aggregate[E], error)
	Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error
	Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error
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
	// This is useful in situations where it is desirable to delay the application of events,
	// such as when wrapping the aggregate store with additional functionality.
	//
	// Default: false
	SkipApply bool
}

type AggregateNotFoundError struct {
	AggregateID typeid.UUID
}

func (e AggregateNotFoundError) Error() string {
	return "aggregate not found: " + e.AggregateID.String()
}

type InitializeAggregateStoreError struct {
	Operation string
	Err       error
}

func (e InitializeAggregateStoreError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// A CreateAggregateError is an error that occurred while creating an aggregate.
type CreateAggregateError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e CreateAggregateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// A LoadAggregateError is an error that occurred while loading an aggregate.
type LoadAggregateError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e LoadAggregateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// A HydrateAggregateError is an error that occurred while hydrating an aggregate.
type HydrateAggregateError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e HydrateAggregateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// A SaveAggregateError is an error that occurred while saving an aggregate.
type SaveAggregateError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e SaveAggregateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}
