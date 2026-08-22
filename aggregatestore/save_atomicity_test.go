package aggregatestore_test

import (
	"errors"
	"testing"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore/memory"
	snapshotmemory "github.com/go-estoria/estoria/snapshotstore/memory"
	"github.com/gofrs/uuid/v5"
)

// These tests pin the meaning of ErrEventsAppended: a save failure carries it if and
// only if the failure came after the events were durably appended. Callers branch on
// it to decide whether persisted state moved ahead of the in-memory aggregate, so
// both directions matter.

// TestSaveAtomicity_SentinelSurvivesComposition forces a post-append apply failure at
// the bottom of the full four-store composition and asserts errors.Is still finds the
// sentinel at the top. Every wrapping store rewraps failures in its own SaveError —
// three deep by the time one reaches the caller — which is the failure mode that
// ruled out a field on SaveError: errors.As would bind the outermost wrapper, whose
// field nobody set.
func TestSaveAtomicity_SentinelSurvivesComposition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store := newComposedStore(t, eventStore, snapshotmemory.NewSnapshotStore(), aggregatestore.NewMemoryAggregateCache[account](), &hookCounts{})

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(fundsDeposited{Amount: 100})

	// Poison the apply queue with an event version the aggregate cannot be at. The
	// append itself succeeds; the post-append apply loop then fails on the queue.
	aggregate.TestOnlyWillApply(&aggregatestore.Event[account]{
		Version:     99,
		DomainEvent: fundsDeposited{Amount: 1},
	})

	err = store.Save(ctx, aggregate, nil)
	if err == nil {
		t.Fatal("want a save error for a poisoned apply queue, got nil")
	}

	if !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Errorf("want errors.Is to find ErrEventsAppended through the composition, got: %v", err)
	}
}

// TestSaveAtomicity_PreAppendFailureCarriesNoSentinel drives the same composition into
// a failure before anything is appended — a stale aggregate losing the optimistic
// concurrency check — and asserts the sentinel is absent: this save wrote nothing.
func TestSaveAtomicity_PreAppendFailureCarriesNoSentinel(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store := newComposedStore(t, eventStore, snapshotmemory.NewSnapshotStore(), aggregatestore.NewMemoryAggregateCache[account](), &hookCounts{})
	accountUUID := uuid.Must(uuid.NewV4())

	aggregate := store.New(accountUUID)
	aggregate.Append(fundsDeposited{Amount: 100})
	if err := store.Save(ctx, aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	stale := store.New(accountUUID)
	stale.Append(fundsDeposited{Amount: 1})

	err = store.Save(ctx, stale, nil)
	if err == nil {
		t.Fatal("want a save error for a stale aggregate, got nil")
	}

	if errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Errorf("want no ErrEventsAppended for a failure that appended nothing, got: %v", err)
	}

	if !errors.Is(err, aggregatestore.ErrNoEventsAppended) {
		t.Errorf("want ErrNoEventsAppended surviving the composition for a refused append, got: %v", err)
	}
}

// TestDiscardUnsavedEvents pins the recovery affordance for failed saves: a
// discarded queue leaves the aggregate at its last saved shape, so a later
// command's save cannot re-append the discarded command's event.
func TestDiscardUnsavedEvents(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(fundsDeposited{Amount: 100})

	if err := store.Save(ctx, aggregate, nil); err != nil {
		t.Fatalf("saving: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 50})
	aggregate.DiscardUnsavedEvents()

	// Nothing pending: the save is a no-op and the aggregate stays at the
	// saved version.
	if err := store.Save(ctx, aggregate, nil); err != nil {
		t.Fatalf("saving after discard: %v", err)
	}

	if got := aggregate.Version(); got != 1 {
		t.Errorf("want version 1 after discarding the queued event, got %d", got)
	}

	aggregate.Append(fundsDeposited{Amount: 25})

	if err := store.Save(ctx, aggregate, nil); err != nil {
		t.Fatalf("saving a later command: %v", err)
	}

	if got := aggregate.Version(); got != 2 {
		t.Errorf("want the later save to persist only its own event (version 2), got %d", got)
	}
}

// TestSaveAtomicity_EventSourcedStoreSite covers EventSourcedStore's own post-append
// apply loop. The composition cannot reach it: SnapshottingStore always saves through
// the inner store with SkipApply and runs the application itself, so this site's
// sentinel is only observable on a bare EventSourcedStore.
func TestSaveAtomicity_EventSourcedStoreSite(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(fundsDeposited{Amount: 100})
	aggregate.TestOnlyWillApply(&aggregatestore.Event[account]{
		Version:     99,
		DomainEvent: fundsDeposited{Amount: 1},
	})

	err = store.Save(ctx, aggregate, nil)
	if err == nil {
		t.Fatal("want a save error for a poisoned apply queue, got nil")
	}

	if !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Errorf("want errors.Is to find ErrEventsAppended, got: %v", err)
	}
}
