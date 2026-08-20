package aggregatestore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// mockCache is a mock implementation of aggregatestore.AggregateCache.
type mockCache[E any] struct {
	GetAggregateFn func(context.Context, typeid.ID) (*aggregatestore.CachedAggregate[E], error)
	PutAggregateFn func(context.Context, typeid.ID, aggregatestore.CachedAggregate[E]) error
}

var _ aggregatestore.AggregateCache[mockEntity] = (*mockCache[mockEntity])(nil)

func (c *mockCache[E]) GetAggregate(ctx context.Context, id typeid.ID) (*aggregatestore.CachedAggregate[E], error) {
	if c.GetAggregateFn != nil {
		return c.GetAggregateFn(ctx, id)
	}

	return nil, fmt.Errorf("unexpected call: GetAggregate(id=%s)", id)
}

func (c *mockCache[E]) PutAggregate(ctx context.Context, id typeid.ID, entry aggregatestore.CachedAggregate[E]) error {
	if c.PutAggregateFn != nil {
		return c.PutAggregateFn(ctx, id, entry)
	}

	return fmt.Errorf("unexpected call: PutAggregate(id=%s, entry=%v)", id, entry)
}

func TestNewCachedStore(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		haveInner func() aggregatestore.Store[mockEntity]
		haveCache func() aggregatestore.AggregateCache[mockEntity]
		wantErr   error
	}{
		{
			name: "creates a new cached store",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStore, gotErr := aggregatestore.NewCachedStore(tt.haveInner(), tt.haveCache())
			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
			} else if gotErr != nil {
				t.Errorf("unexpected error: %v", gotErr)
			}

			if gotStore == nil {
				t.Error("unexpected nil store")
			}
		})
	}
}

func TestCachedStore_Load(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	for _, tt := range []struct {
		name          string
		haveInner     func() aggregatestore.Store[mockEntity]
		haveCache     func() aggregatestore.AggregateCache[mockEntity]
		haveOpts      *aggregatestore.LoadOptions
		wantAggregate *aggregatestore.Aggregate[mockEntity]
		wantErr       error
	}{
		{
			name: "returns an aggregate from the cache when available",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
						return newMockAggregate(id, 0)
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					GetAggregateFn: func(_ context.Context, id typeid.ID) (*aggregatestore.CachedAggregate[mockEntity], error) {
						return &aggregatestore.CachedAggregate[mockEntity]{State: newMockEntity(id.UUID), Version: 42}, nil
					},
				}
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "returns an aggregate from the inner store when the aggregate is not found in the cache",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
						return newMockAggregate(id, 0)
					},
					LoadFn: func(_ context.Context, id uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
						return newMockAggregate(id, 42), nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					GetAggregateFn: func(context.Context, typeid.ID) (*aggregatestore.CachedAggregate[mockEntity], error) {
						return nil, nil
					},
				}
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "returns an aggregate from the inner store when the cache returns an error",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
						return newMockAggregate(id, 0)
					},
					LoadFn: func(_ context.Context, id uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
						return newMockAggregate(id, 42), nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					GetAggregateFn: func(context.Context, typeid.ID) (*aggregatestore.CachedAggregate[mockEntity], error) {
						return nil, errors.New("mock error")
					},
				}
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
						return newMockAggregate(id, 0)
					},
					LoadFn: func(context.Context, uuid.UUID, *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
						return nil, errors.New("mock error")
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					GetAggregateFn: func(context.Context, typeid.ID) (*aggregatestore.CachedAggregate[mockEntity], error) {
						return nil, nil
					},
				}
			},
			wantErr: errors.New("loading from inner aggregate store: mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewCachedStore(tt.haveInner(), tt.haveCache())
			if store == nil {
				t.Fatal("unexpected nil store")
			} else if err != nil {
				t.Fatalf("unexpected error creating cached store: %v", err)
			}

			gotAggregate, gotErr := store.Load(t.Context(), aggregateID, tt.haveOpts)

			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if gotErr != nil {
				t.Errorf("unexpected error: %v", gotErr)
			} else if gotAggregate == nil {
				t.Errorf("unexpected nil aggregate")
			}

			// aggregate has the correct ID
			if gotAggregate.ID().String() != typeid.New("mockentity", aggregateID).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.New("mockentity", aggregateID), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantAggregate.Version() {
				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
			}
		})
	}
}

func TestCachedStore_Hydrate(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	for _, tt := range []struct {
		name          string
		haveInner     func() aggregatestore.Store[mockEntity]
		haveCache     func() aggregatestore.AggregateCache[mockEntity]
		haveOpts      *aggregatestore.HydrateOptions
		haveAggregate func() *aggregatestore.Aggregate[mockEntity]
		wantAggregate *aggregatestore.Aggregate[mockEntity]
		wantErr       error
	}{
		{
			name: "hydrates an aggregate using the inner store",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
						aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), 42)
						return nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 0)
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					HydrateFn: func(context.Context, *aggregatestore.Aggregate[mockEntity], *aggregatestore.HydrateOptions) error {
						return errors.New("mock error")
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return nil
			},
			wantErr: errors.New("mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewCachedStore(tt.haveInner(), tt.haveCache())
			if store == nil {
				t.Fatal("unexpected nil store")
			} else if err != nil {
				t.Fatalf("unexpected error creating cached store: %v", err)
			}

			gotAggregate := tt.haveAggregate()
			gotErr := store.Hydrate(t.Context(), gotAggregate, tt.haveOpts)

			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if gotErr != nil {
				t.Errorf("unexpected error: %v", gotErr)
			} else if gotAggregate == nil {
				t.Errorf("unexpected nil aggregate")
			}

			// aggregate has the correct ID
			if gotAggregate.ID().String() != typeid.New("mockentity", aggregateID).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.New("mockentity", aggregateID), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantAggregate.Version() {
				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
			}
		})
	}
}

func TestCachedStore_Save(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	for _, tt := range []struct {
		name                string
		haveInner           func() aggregatestore.Store[mockEntity]
		haveCache           func() aggregatestore.AggregateCache[mockEntity]
		haveOpts            *aggregatestore.SaveOptions
		haveAggregate       func() *aggregatestore.Aggregate[mockEntity]
		wantAggregate       *aggregatestore.Aggregate[mockEntity]
		wantCachedAggregate *aggregatestore.Aggregate[mockEntity]
		wantErr             error
	}{
		{
			name: "saves an aggregate using the inner store and adds the aggregate to the cache",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
						aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), 42)
						return nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					PutAggregateFn: func(context.Context, typeid.ID, aggregatestore.CachedAggregate[mockEntity]) error {
						return nil
					},
				}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 0)
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
			wantCachedAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					SaveFn: func(context.Context, *aggregatestore.Aggregate[mockEntity], *aggregatestore.SaveOptions) error {
						return errors.New("mock error")
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			},
			wantErr: errors.New("saving to inner aggregate store: mock error"),
		},
		{
			name: "does not return an error when failing to add the aggregate to the cache",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
						aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), 42)
						return nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					PutAggregateFn: func(context.Context, typeid.ID, aggregatestore.CachedAggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 0)
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			haveCache := tt.haveCache()

			store, err := aggregatestore.NewCachedStore(tt.haveInner(), haveCache)
			if store == nil {
				t.Fatal("unexpected nil store")
			} else if err != nil {
				t.Fatalf("unexpected error creating cached store: %v", err)
			}

			gotAggregate := tt.haveAggregate()
			gotErr := store.Save(t.Context(), gotAggregate, tt.haveOpts)

			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if gotErr != nil {
				t.Errorf("unexpected error: %v", gotErr)
			} else if gotAggregate == nil {
				t.Errorf("unexpected nil aggregate")
			}

			// aggregate has the correct ID
			if gotAggregate.ID().String() != typeid.New("mockentity", aggregateID).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.New("mockentity", aggregateID), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantAggregate.Version() {
				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
			}

			if tt.wantCachedAggregate == nil {
				return
			}
		})
	}
}

// TestNewCachedStore_RejectsNilCache guards against construction succeeding with no cache,
// which deferred the failure to a nil-map panic on the first Load.
func TestNewCachedStore_RejectsNilCache(t *testing.T) {
	t.Parallel()

	store, err := aggregatestore.NewCachedStore[mockEntity](&mockAggregateStore[mockEntity]{}, nil)
	if err == nil {
		t.Fatal("want an error constructing a cached store with a nil cache, got nil")
	}
	if store != nil {
		t.Error("want a nil store alongside the error")
	}
}

// TestCachedStore_Load_BypassesCacheForVersionedLoad guards against a cache hit short-
// circuiting a load that asked for a specific version. The cache holds whatever version was
// last saved, so serving it for a ToVersion load silently returns the wrong state.
//
// The cache must also not be written on this path: storing a deliberately-truncated
// aggregate would serve that stale version to later full loads.
func TestCachedStore_Load_BypassesCacheForVersionedLoad(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	var cacheReads, cacheWrites int

	inner := &mockAggregateStore[mockEntity]{
		NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
			return newMockAggregate(id, 0)
		},
		LoadFn: func(_ context.Context, id uuid.UUID, opts *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
			if opts == nil || opts.ToVersion == 0 {
				return newMockAggregate(id, 10), nil
			}
			return newMockAggregate(id, opts.ToVersion), nil
		},
	}

	cache := &mockCache[mockEntity]{
		GetAggregateFn: func(_ context.Context, id typeid.ID) (*aggregatestore.CachedAggregate[mockEntity], error) {
			cacheReads++
			return &aggregatestore.CachedAggregate[mockEntity]{State: newMockEntity(id.UUID), Version: 10}, nil
		},
		PutAggregateFn: func(context.Context, typeid.ID, aggregatestore.CachedAggregate[mockEntity]) error {
			cacheWrites++
			return nil
		},
	}

	store, err := aggregatestore.NewCachedStore[mockEntity](inner, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	got, err := store.Load(t.Context(), aggregateID, &aggregatestore.LoadOptions{ToVersion: 3})
	if err != nil {
		t.Fatalf("loading aggregate: %v", err)
	}

	if want := int64(3); got.Version() != want {
		t.Errorf("want version %d from a versioned load, got %d", want, got.Version())
	}
	if cacheReads != 0 {
		t.Errorf("want the cache not read for a versioned load, got %d reads", cacheReads)
	}
	if cacheWrites != 0 {
		t.Errorf("want the cache not written for a versioned load, got %d writes", cacheWrites)
	}

	// An unversioned load still uses the cache.
	if _, err := store.Load(t.Context(), aggregateID, nil); err != nil {
		t.Fatalf("loading aggregate without options: %v", err)
	}
	if cacheReads != 1 {
		t.Errorf("want 1 cache read for an unversioned load, got %d", cacheReads)
	}
}

// TestCachedStore_Save_NilAggregate guards against Save dereferencing a nil aggregate,
// where every other store in this package returns ErrNilAggregate.
func TestCachedStore_Save_NilAggregate(t *testing.T) {
	t.Parallel()

	store, err := aggregatestore.NewCachedStore[mockEntity](
		&mockAggregateStore[mockEntity]{}, &mockCache[mockEntity]{})
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	if err := store.Save(t.Context(), nil, nil); !errors.Is(err, aggregatestore.ErrNilAggregate) {
		t.Errorf("want ErrNilAggregate, got %v", err)
	}
}

// gatedPutCache parks one armed put — the first whose entry version matches
// gateVersion — inside PutAggregate until released, delegating everything
// else to the inner cache untouched.
type gatedPutCache[S any] struct {
	inner       aggregatestore.AggregateCache[S]
	mu          sync.Mutex
	armed       bool
	gateVersion int64
	entered     chan struct{}
	release     chan struct{}
}

func (c *gatedPutCache[S]) GetAggregate(ctx context.Context, id typeid.ID) (*aggregatestore.CachedAggregate[S], error) {
	return c.inner.GetAggregate(ctx, id)
}

func (c *gatedPutCache[S]) PutAggregate(ctx context.Context, id typeid.ID, entry aggregatestore.CachedAggregate[S]) error {
	c.mu.Lock()
	gate := c.armed && entry.Version == c.gateVersion
	if gate {
		c.armed = false
	}
	c.mu.Unlock()

	if gate {
		close(c.entered)
		<-c.release
	}

	return c.inner.PutAggregate(ctx, id, entry)
}

// TestCachedStore_ConcurrentSavesCannotRegressTheCache pins cache publication
// against out-of-order completion: command A's save persists version 2 but
// parks inside its cache write; command B reads version 2 from the store,
// persists version 3, and publishes it; A's write then lands last, carrying
// the older version. Publication must not regress the cache — a hit is served
// as-is, so a regressed entry would serve the stale state indefinitely. The
// equal-version boundary is not separately pinned: optimistic concurrency
// admits one save per version, so an entry can only ever be displaced by a
// different version.
func TestCachedStore_ConcurrentSavesCannotRegressTheCache(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	base, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}, fundsWithdrawn{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	gated := &gatedPutCache[account]{
		inner:       newMapAggregateCache[account](),
		armed:       true,
		gateVersion: 2,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}

	cached, err := aggregatestore.NewCachedStore(base, gated)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())

	// Seed version 1 so both commands work over an existing stream.
	seed := cached.New(id)
	seed.Append(fundsDeposited{Amount: 10})
	if err := cached.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// Command A: event-only read, append, save. Its cache write (version 2)
	// parks inside the gate after the inner save has persisted.
	saved := make(chan error, 1)
	go func() {
		aggregate, err := base.Load(context.Background(), id, nil)
		if err != nil {
			saved <- err
			return
		}

		aggregate.Append(fundsDeposited{Amount: 5})
		saved <- cached.Save(context.Background(), aggregate, nil)
	}()

	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for command A's cache write to park")
	}

	// Command B: reads version 2 (A's append is durable), publishes
	// version 3; its write lands while A's is still parked.
	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("command B load: %v", err)
	}

	if aggregate.Version() != 2 {
		t.Fatalf("want command B reading version 2, got %d", aggregate.Version())
	}

	aggregate.Append(fundsWithdrawn{Amount: 3})
	if err := cached.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("command B save: %v", err)
	}

	// Release A's parked write: it lands last, carrying the older version.
	close(gated.release)
	if err := <-saved; err != nil {
		t.Fatalf("command A save: %v", err)
	}

	// The cache must not regress: a hit serves the newest published state.
	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 3 {
		t.Errorf("want the cache serving version 3, got %d (the cache regressed)", loaded.Version())
	}

	if balance := loaded.State().Balance; balance != 12 {
		t.Errorf("want the newest state (balance 12), got %d", balance)
	}

	// The backend entry heals too: a refused-as-stale hit republishes the
	// version already at the high-water mark, so the regressed entry cannot
	// outlive the load that detected it.
	entry, err := gated.inner.GetAggregate(t.Context(), loaded.ID())
	if err != nil || entry == nil {
		t.Fatalf("reading the backing cache entry: %+v, %v", entry, err)
	}

	if entry.Version != 3 {
		t.Errorf("want the backing entry healed to version 3, got %d", entry.Version)
	}
}

// gatedLoadStore delegates and, when armed, parks one Load between the inner
// read and its return, holding the caller's subsequent cache publication open
// while newer publications land.
type gatedLoadStore struct {
	aggregatestore.Store[account]
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (s *gatedLoadStore) Load(ctx context.Context, id uuid.UUID, opts *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[account], error) {
	aggregate, err := s.Store.Load(ctx, id, opts)

	s.mu.Lock()
	gate := s.armed
	s.armed = false
	s.mu.Unlock()

	if gate {
		close(s.entered)
		<-s.release
	}

	return aggregate, err
}

// TestCachedStore_LateLoadPublicationCannotRegressTheCache pins the other
// publication race: a cache-miss load reads version 2 from the inner store
// and parks before publishing; a save then persists and publishes version 3;
// the load's whole publication — high-water mark and write alike — runs
// last, carrying the older version. It must not displace the newer entry or
// regress the mark it is compared against.
func TestCachedStore_LateLoadPublicationCannotRegressTheCache(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	base, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}, fundsWithdrawn{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	// Version 2 exists before the cache sees anything: both saves go through
	// the base store, so the reader below misses and reads the inner store.
	id := uuid.Must(uuid.NewV4())
	seed := base.New(id)
	seed.Append(fundsDeposited{Amount: 10}, fundsDeposited{Amount: 5})
	if err := base.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	gatedInner := &gatedLoadStore{
		Store:   base,
		armed:   true,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}

	cached, err := aggregatestore.NewCachedStore(aggregatestore.Store[account](gatedInner), newMapAggregateCache[account]())
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	// The reader misses, reads version 2 from the inner store, and parks
	// before returning — its publication now trails everything below.
	read := make(chan error, 1)
	go func() {
		_, err := cached.Load(context.Background(), id, nil)
		read <- err
	}()

	select {
	case <-gatedInner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the reader to park")
	}

	// The writer persists and publishes version 3 while the reader is parked.
	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("writer load: %v", err)
	}

	aggregate.Append(fundsWithdrawn{Amount: 3})
	if err := cached.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("writer save: %v", err)
	}

	// Release the reader: its version-2 publication runs entirely after the
	// version-3 one.
	close(gatedInner.release)
	if err := <-read; err != nil {
		t.Fatalf("reader load: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 3 {
		t.Errorf("want the cache serving version 3, got %d (the late publication regressed it)", loaded.Version())
	}

	if balance := loaded.State().Balance; balance != 12 {
		t.Errorf("want the newest state (balance 12), got %d", balance)
	}
}
