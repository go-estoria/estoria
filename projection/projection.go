// Package projection provides the building blocks for maintaining read models
// from an event store: a versioned projection identity, handlers that apply
// events to read-side state, and a one-shot fold over an event stream.
// Persistence of projection progress lives in the checkpointstore subpackage.
package projection

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/go-estoria/estoria/eventstore"
)

// An ID identifies one version of a named projection. Checkpoints, cutover,
// and rebuild history are keyed by it: two versions of the same name are
// distinct projections that can process the same events concurrently, each
// under its own checkpoint.
type ID struct {
	// Name identifies the projection independent of version, e.g. "orders".
	Name string

	// Version distinguishes successive rebuilds of the projection's read
	// model. Versions are 1-based.
	Version int
}

// String returns the canonical form, e.g. "orders_v7". It is the single form
// under which a projection is named everywhere: checkpoint keys, log output,
// and the storage suffix a handler uses to derive versioned table, index, or
// collection names.
func (id ID) String() string {
	return id.Name + "_v" + strconv.Itoa(id.Version)
}

// MaxNameLength is the longest allowed projection name. The bound leaves room
// within common identifier limits (PostgreSQL truncates names at 63 bytes) for
// the version suffix and a handler's own prefixing.
const MaxNameLength = 40

// Validate reports whether the ID can serve as a projection identity. The name
// must start with a lowercase letter, contain only lowercase letters, digits,
// and underscores, not end with an underscore, and be at most MaxNameLength
// characters, so that String is always safe as an unquoted storage identifier.
// The version must be positive.
func (id ID) Validate() error {
	switch {
	case id.Name == "":
		return errors.New("projection name is required")
	case len(id.Name) > MaxNameLength:
		return fmt.Errorf("projection name must be at most %d characters", MaxNameLength)
	case id.Name[0] < 'a' || id.Name[0] > 'z':
		return errors.New("projection name must start with a lowercase letter")
	case id.Name[len(id.Name)-1] == '_':
		return errors.New("projection name must not end with '_'")
	case id.Version < 1:
		return errors.New("projection version must be positive")
	}

	for _, c := range id.Name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return fmt.Errorf("projection name must not contain %q", c)
		}
	}

	return nil
}

// An EventHandler handles an individual event.
type EventHandler interface {
	Handle(ctx context.Context, event *eventstore.Event) error
}

// A Teardowner is implemented by event handlers that can remove the versioned
// storage they own (drop a table, delete an index). Retiring a projection
// version's predecessor requires the capability: the lifecycle orchestrator
// discovers it by type assertion and refuses the retirement without it.
// Teardown must be idempotent — tearing down storage that is already absent
// must succeed — because retirement retries it after partial failures, and
// concurrent-safe for the same ID: retries run from whichever handles hold
// the reserved retirement, nothing serializes them across processes, and
// overlapping teardowns of the same version must not corrupt or fail
// spuriously.
type Teardowner interface {
	Teardown(ctx context.Context, id ID) error
}

// An EventHandlerFunc is a function that handles an event during projection.
type EventHandlerFunc func(ctx context.Context, event *eventstore.Event) error

// Handle implements the EventHandler interface, allowing an EventHandlerFunc to be used as an EventHandler.
func (f EventHandlerFunc) Handle(ctx context.Context, event *eventstore.Event) error {
	return f(ctx, event)
}
