package aggregatestore

import (
	"context"
	"errors"

	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// A Store is a read/write store for aggregates.
type Store[S any] interface {
	// AggregateType returns the aggregate type name used to compose the typed IDs
	// under which the store's aggregates are addressed.
	AggregateType() string
	New(id uuid.UUID) *Aggregate[S]
	Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[S], error)
	Hydrate(ctx context.Context, aggregate *Aggregate[S], opts *HydrateOptions) error
	Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error
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

// Validate validates the LoadOptions.
func (o LoadOptions) Validate() error {
	if o.ToVersion < 0 {
		return errors.New("ToVersion cannot be negative")
	}

	return nil
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

// Validate validates the HydrateOptions.
func (o HydrateOptions) Validate() error {
	if o.ToVersion < 0 {
		return errors.New("ToVersion cannot be negative")
	}

	return nil
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

// An InitializeError is an error that occurred while initializing an aggregate store.
type InitializeError struct {
	Operation string
	Err       error
}

// Error implements the error interface.
func (e InitializeError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e InitializeError) Unwrap() error {
	return e.Err
}

// A CreateError is an error that occurred while creating an aggregate.
type CreateError struct {
	AggregateID typeid.ID
	Operation   string
	Err         error
}

// Error implements the error interface.
func (e CreateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e CreateError) Unwrap() error {
	return e.Err
}

// A LoadError is an error that occurred while loading an aggregate.
type LoadError struct {
	AggregateID typeid.ID
	Operation   string
	Err         error
}

// Error implements the error interface.
func (e LoadError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}
	return e.Operation + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e LoadError) Unwrap() error {
	return e.Err
}

// A HydrateError is an error that occurred while hydrating an aggregate.
type HydrateError struct {
	AggregateID typeid.ID
	Operation   string
	Err         error
}

// NewHydrateError creates a new HydrateError.
func NewHydrateError(id typeid.ID, operation string, err error) HydrateError {
	return HydrateError{
		AggregateID: id,
		Operation:   operation,
		Err:         err,
	}
}

// Error implements the error interface.
func (e HydrateError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e HydrateError) Unwrap() error {
	return e.Err
}

// A SaveError is an error that occurred while saving an aggregate.
type SaveError struct {
	AggregateID typeid.ID
	Operation   string
	Err         error
}

// saveOpValidatingVersion names the save step that guards the version
// arithmetic, applied identically by every store that performs it.
const saveOpValidatingVersion = "validating aggregate version"

// Error implements the error interface.
func (e SaveError) Error() string {
	if e.Operation == "" {
		return e.Err.Error()
	}

	return e.Operation + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e SaveError) Unwrap() error {
	return e.Err
}

// ErrAggregateNotFound indicates that an aggregate was not found in the aggregate store.
var ErrAggregateNotFound = errors.New("aggregate not found")

// ErrEventsAppended reports a save that failed after its events were durably
// appended to the event store: the events are facts in storage, but the save
// did not complete — the aggregate's in-memory state may not reflect them, or
// a step after the append failed. Recover by discarding the aggregate and
// reloading it, which replays the appended events.
//
// Check for it with errors.Is, which finds it through any amount of wrapping.
var ErrEventsAppended = errors.New("events appended but not applied to the aggregate")

// ErrNoEventsAppended reports a save that failed with nothing appended to the
// event store: the aggregate's queued events remain exactly as they were, and
// the save may simply be retried.
//
// Check for it with errors.Is. A save error carrying neither
// ErrNoEventsAppended nor ErrEventsAppended reports an unknown outcome: the
// append may or may not have become durable — a store can commit and lose its
// response — and only reading the stream resolves it.
//
// A Store decorator must preserve these markers when wrapping an inner save
// error (wrap with %w) and must mark the save errors it originates: an error
// raised before delegating inward appended nothing, and one raised after a
// successful inner save follows events that are already facts.
var ErrNoEventsAppended = errors.New("no events were appended")

// withSaveOutcome attaches outcome — ErrEventsAppended or ErrNoEventsAppended —
// to err for errors.Is without restating it in the message: the message
// already names the failure, and the marker is contract, not prose.
func withSaveOutcome(outcome, err error) error {
	return saveOutcomeError{outcome: outcome, err: err}
}

type saveOutcomeError struct {
	outcome error
	err     error
}

func (e saveOutcomeError) Error() string { return e.err.Error() }

func (e saveOutcomeError) Unwrap() error { return e.err }

func (e saveOutcomeError) Is(target error) bool { return target == e.outcome }

// ErrNilAggregate indicates that the provided aggregate is nil.
var ErrNilAggregate = errors.New("aggregate is nil")
