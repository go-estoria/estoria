package aggregatestore_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/snapshotstore"
	snapshotmemory "github.com/go-estoria/estoria/snapshotstore/memory"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// This test pins the observable behavior of the full aggregate store composition
// (EventSourcedStore → SnapshottingStore → CachedStore) so that refactors of the
// aggregatestore package are measured against it. Only mechanical renames may
// touch this file while such a refactor is in flight.

type account struct {
	ID      typeid.ID
	Balance int
}

func newAccount(id uuid.UUID) account {
	return account{ID: typeid.New("account", id)}
}

type fundsDeposited struct {
	Amount int
}

func (fundsDeposited) EventType() string { return "fundsdeposited" }

func (fundsDeposited) New() estoria.DomainEvent[account] { return &fundsDeposited{} }

func (e fundsDeposited) ApplyTo(a account) account {
	a.Balance += e.Amount
	return a
}

type fundsWithdrawn struct {
	Amount int
}

func (fundsWithdrawn) EventType() string { return "fundswithdrawn" }

func (fundsWithdrawn) New() estoria.DomainEvent[account] { return &fundsWithdrawn{} }

func (e fundsWithdrawn) ApplyTo(a account) account {
	a.Balance -= e.Amount
	return a
}

// mapAggregateCache stores entries the way the contrib caches do: state and version,
// keyed by typed ID. Identity must survive the round trip through the cache.
type mapAggregateCache[S any] struct {
	mu      sync.RWMutex
	entries map[string]aggregatestore.CachedAggregate[S]
}

func newMapAggregateCache[S any]() *mapAggregateCache[S] {
	return &mapAggregateCache[S]{entries: make(map[string]aggregatestore.CachedAggregate[S])}
}

func (c *mapAggregateCache[S]) GetAggregate(_ context.Context, aggregateID typeid.ID) (*aggregatestore.CachedAggregate[S], error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[aggregateID.String()]
	if !ok {
		return nil, nil //nolint:nilnil // a nil entry with a nil error is the cache-miss contract
	}

	return &entry, nil
}

func (c *mapAggregateCache[S]) PutAggregate(_ context.Context, aggregateID typeid.ID, entry aggregatestore.CachedAggregate[S]) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[aggregateID.String()] = entry

	return nil
}

var _ aggregatestore.AggregateCache[account] = (*mapAggregateCache[account])(nil)

func newComposedStore(
	t *testing.T,
	eventStore *memory.EventStore,
	snapshotStore *snapshotmemory.SnapshotStore,
	cache aggregatestore.AggregateCache[account],
) *aggregatestore.CachedStore[account] {
	t.Helper()

	eventSourced, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}, fundsWithdrawn{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	snapshotting, err := aggregatestore.NewSnapshottingStore(eventSourced, snapshotStore,
		snapshotstore.EventCountSnapshotPolicy{N: 3})
	if err != nil {
		t.Fatalf("creating snapshotting store: %v", err)
	}

	cached, err := aggregatestore.NewCachedStore(snapshotting, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	return cached
}

func TestEndToEnd_FullComposition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	snapshotStore := snapshotmemory.NewSnapshotStore()
	cache := newMapAggregateCache[account]()
	store := newComposedStore(t, eventStore, snapshotStore, cache)

	accountUUID := uuid.Must(uuid.NewV4())
	wantID := typeid.New("account", accountUUID)

	// Create an aggregate, append events across two saves, and verify state.
	// The second save crosses version 3, where the snapshot policy fires.
	aggregate := store.New(accountUUID)
	if got := aggregate.ID(); got.String() != wantID.String() {
		t.Fatalf("new aggregate ID: want %s, got %s", wantID, got)
	}

	aggregate.Append(fundsDeposited{Amount: 100}, fundsDeposited{Amount: 50})

	if err := store.Save(ctx, aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	if got := aggregate.Version(); got != 2 {
		t.Fatalf("version after first save: want 2, got %d", got)
	}

	aggregate.Append(fundsWithdrawn{Amount: 30}, fundsDeposited{Amount: 10}, fundsDeposited{Amount: 5})

	if err := store.Save(ctx, aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	if got := aggregate.Version(); got != 5 {
		t.Fatalf("version after second save: want 5, got %d", got)
	}

	if got := aggregate.State().Balance; got != 135 {
		t.Fatalf("balance after second save: want 135, got %d", got)
	}

	// The snapshot policy fired at version 3.
	snap, err := snapshotStore.ReadSnapshot(ctx, wantID, snapshotstore.ReadSnapshotOptions{})
	if err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}

	if snap.AggregateVersion != 3 {
		t.Errorf("snapshot version: want 3, got %d", snap.AggregateVersion)
	}

	// A load through the full stack hits the cache; the rebuilt aggregate must
	// preserve identity, version, and state.
	loaded, err := store.Load(ctx, accountUUID, nil)
	if err != nil {
		t.Fatalf("loading aggregate: %v", err)
	}

	if got := loaded.ID(); got.String() != wantID.String() {
		t.Errorf("cached load ID: want %s, got %s", wantID, got)
	}

	if got := loaded.Version(); got != 5 {
		t.Errorf("cached load version: want 5, got %d", got)
	}

	if got := loaded.State().Balance; got != 135 {
		t.Errorf("cached load balance: want 135, got %d", got)
	}

	// A load with a cold cache hydrates from the snapshot plus the events past
	// it and must converge on the same state.
	coldStore := newComposedStore(t, eventStore, snapshotStore, newMapAggregateCache[account]())

	reloaded, err := coldStore.Load(ctx, accountUUID, nil)
	if err != nil {
		t.Fatalf("loading aggregate with cold cache: %v", err)
	}

	if got := reloaded.ID(); got.String() != wantID.String() {
		t.Errorf("cold load ID: want %s, got %s", wantID, got)
	}

	if got := reloaded.Version(); got != 5 {
		t.Errorf("cold load version: want 5, got %d", got)
	}

	if got := reloaded.State().Balance; got != 135 {
		t.Errorf("cold load balance: want 135, got %d", got)
	}

	// Time travel bypasses cache and snapshot (none exists at or below version
	// 2) and replays events from the beginning.
	timeTraveled, err := store.Load(ctx, accountUUID, &aggregatestore.LoadOptions{ToVersion: 2})
	if err != nil {
		t.Fatalf("loading aggregate to version 2: %v", err)
	}

	if got := timeTraveled.Version(); got != 2 {
		t.Errorf("time-traveled version: want 2, got %d", got)
	}

	if got := timeTraveled.State().Balance; got != 150 {
		t.Errorf("time-traveled balance: want 150, got %d", got)
	}

	// Loading an unknown aggregate reports ErrAggregateNotFound through every layer.
	if _, err := store.Load(ctx, uuid.Must(uuid.NewV4()), nil); !errors.Is(err, aggregatestore.ErrAggregateNotFound) {
		t.Errorf("loading unknown aggregate: want ErrAggregateNotFound, got %v", err)
	}
}
