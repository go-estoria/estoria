package aggregatestore

import (
	"context"
	"errors"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// A Store is a read/write store for aggregates.
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

var ErrNilAggregate = errors.New("aggregate is nil")

// ErrAggregateNotFound indicates that an aggregate was not found in the aggregate store.
var ErrAggregateNotFound = errors.New("aggregate not found")

type InitializeError struct {
	Operation string
	Err       error
}

func (e InitializeError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// A CreateError is an error that occurred while creating an aggregate.
type CreateError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e CreateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// A LoadError is an error that occurred while loading an aggregate.
type LoadError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e LoadError) Error() string {
	return e.Operation + ": " + e.Err.Error()
}

// A HydrateError is an error that occurred while hydrating an aggregate.
type HydrateError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e HydrateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// A SaveError is an error that occurred while saving an aggregate.
type SaveError struct {
	AggregateID typeid.UUID
	Operation   string
	Err         error
}

func (e SaveError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}
