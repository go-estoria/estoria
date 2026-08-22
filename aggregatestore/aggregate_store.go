package aggregatestore

import (
	"context"
	"errors"
	"fmt"

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
// reloading it, which replays the appended events. It vouches for the append
// alone, nothing about what was or was not applied.
//
// Resolve a save error's outcome with SaveOutcome; see ErrNoEventsAppended
// for the marking duty a Store decorator carries.
var ErrEventsAppended = errors.New("events were appended")

// ErrNoEventsAppended reports a save that failed with nothing appended to the
// event store: the stream is exactly as it was. It vouches for the stream
// only — decorators are not transactional, so a hook or decorator that ran
// before the failure may have mutated the aggregate or queued events of its
// own, and a retried save persists whatever is queued at retry time, not
// necessarily the original command.
//
// Resolve a save error's outcome with SaveOutcome. A save error resolving to
// neither marker reports an unknown outcome: the append may or may not have
// become durable — a store can commit and lose its response — and only
// reading the stream resolves it.
//
// A Store decorator must preserve these markers when wrapping an inner save
// error (wrap with %w) and must mark the save errors it originates: an error
// raised before delegating inward appended nothing, and one raised after a
// successful inner save that appended events follows facts already in the
// stream.
var ErrNoEventsAppended = errors.New("no events were appended")

// An AppendOutcome is the single verdict a save error vouches for about its
// append: the events definitely reached the stream, definitely did not, or
// neither.
type AppendOutcome int

const (
	// AppendOutcomeUnknown vouches for neither outcome: the append may have
	// become durable and lost its response, and only reading the stream
	// resolves it.
	AppendOutcomeUnknown AppendOutcome = iota

	// AppendOutcomeAppended reports the events as durable facts in the
	// stream.
	AppendOutcomeAppended

	// AppendOutcomeNothingAppended reports the stream exactly as it was.
	AppendOutcomeNothingAppended
)

// String describes the outcome.
func (o AppendOutcome) String() string {
	switch o {
	case AppendOutcomeAppended:
		return "appended"
	case AppendOutcomeNothingAppended:
		return "nothing appended"
	case AppendOutcomeUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("AppendOutcome(%d)", int(o))
	}
}

// SaveOutcome resolves the single append outcome a save error vouches for.
// Resolution is outermost-first — the marker nearest the caller speaks for
// the save as a whole and shadows anything deeper, because a decorator's
// error may wrap a cause that carries the opposite marker from some other
// save — and markers that first appear at the same depth and disagree
// resolve to unknown: a contradictory error vouches for nothing. Outcome
// decisions must use this resolver; errors.Is searches the entire chain and
// can confirm both sentinels on the same error.
//
// The resolver is the traversal: it matches sentinel and marker identities
// node by node, outermost first, exactly where errors.Is would search the
// whole chain and defeat the shadowing it exists for.
//
//nolint:errorlint // identity matching per node is the resolver's design
func SaveOutcome(err error) AppendOutcome {
	switch err {
	case nil:
		return AppendOutcomeUnknown
	case ErrEventsAppended:
		return AppendOutcomeAppended
	case ErrNoEventsAppended:
		return AppendOutcomeNothingAppended
	}

	if marked, ok := err.(saveOutcomeError); ok {
		return marked.resolved()
	}

	var children []error

	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		if child := wrapped.Unwrap(); child != nil {
			children = []error{child}
		}
	case interface{ Unwrap() []error }:
		children = wrapped.Unwrap()
	}

	outcome := AppendOutcomeUnknown

	for _, child := range children {
		resolved := SaveOutcome(child)
		if resolved == AppendOutcomeUnknown {
			continue
		}

		if outcome != AppendOutcomeUnknown && outcome != resolved {
			return AppendOutcomeUnknown
		}

		outcome = resolved
	}

	return outcome
}

// withSaveOutcome attaches outcome — ErrEventsAppended or ErrNoEventsAppended —
// to err for SaveOutcome and errors.Is without restating it in the message:
// the message already names the failure, and the marker is contract, not
// prose. The marker shadows any marker deeper in err's chain.
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

// resolved maps the marker to its structured outcome.
//
//nolint:errorlint // the outcome field holds a bare sentinel by construction.
func (e saveOutcomeError) resolved() AppendOutcome {
	if e.outcome == ErrEventsAppended {
		return AppendOutcomeAppended
	}

	return AppendOutcomeNothingAppended
}

// ErrNilAggregate indicates that the provided aggregate is nil.
var ErrNilAggregate = errors.New("aggregate is nil")
