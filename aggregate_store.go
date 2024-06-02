package estoria

import (
	"context"
	"errors"

	"go.jetpack.io/typeid"
)

// An AggregateStore is a read/write store for aggregates.
// See package `aggregatestore` for various implementations.
type AggregateStore[E Entity] interface {
	NewAggregate() (*Aggregate[E], error)
	Allow(prototypes ...EventData)
	Load(ctx context.Context, id typeid.AnyID, opts LoadAggregateOptions) (*Aggregate[E], error)
	Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateAggregateOptions) error
	Save(ctx context.Context, aggregate *Aggregate[E], opts SaveAggregateOptions) error
}

// LoadAggregateOptions are options for loading an aggregate.
type LoadAggregateOptions struct {
	// ToVersion is the version to load the aggregate to.
	//
	// Default: 0 (load to the latest version)
	ToVersion int64

	// ToTime is the time to load the aggregate to.
	//
	// Default: zero time (load to the latest version)
	// ToTime time.Time
}

// HydrateAggregateOptions are options for hydrating an aggregate.
type HydrateAggregateOptions struct {
	// ToVersion is the version to hydrate the aggregate to.
	//
	// Default: 0 (hydrate to the latest version)
	ToVersion int64

	// ToTime is the time to hydrate the aggregate to.
	//
	// Default: zero time (hydrate to the latest version)
	// ToTime time.Time
}

// SaveAggregateOptions are options for saving an aggregate.
type SaveAggregateOptions struct {
	// SkipApply skips applying the events to the entity.
	// This is useful in situations where it is desireable to delay the application of events,
	// such as when wrapping the aggregate store with additional functionality.
	//
	// Default: false
	SkipApply bool
}

type EventDataSerde interface {
	Unmarshal(b []byte, d EventData) error
	Marshal(d EventData) ([]byte, error)
}

// ErrStreamNotFound is returned when a stream is not found.
var ErrStreamNotFound = errors.New("stream not found")

// ErrAggregateNotFound is returned when an aggregate is not found.
var ErrAggregateNotFound = errors.New("aggregate not found")
