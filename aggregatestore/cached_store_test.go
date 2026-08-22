package aggregatestore_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// mockCache is a mock implementation of aggregatestore.AggregateCache.
type mockCache[E any] struct {
	GetAggregateFn func(context.Context, typeid.ID) (*aggregatestore.CachedAggregate[E], error)
	PutAggregateFn func(context.Context, typeid.ID, aggregatestore.CachedAggregate[E]) error
	ReserveFenceFn func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error
	CommitFenceFn  func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error
	ReleaseFenceFn func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error
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

func (c *mockCache[E]) ReserveFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	if c.ReserveFenceFn != nil {
		return c.ReserveFenceFn(ctx, id, version, token)
	}

	return fmt.Errorf("unexpected call: ReserveFence(id=%s, version=%d)", id, version)
}

func (c *mockCache[E]) CommitFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	if c.CommitFenceFn != nil {
		return c.CommitFenceFn(ctx, id, version, token)
	}

	return fmt.Errorf("unexpected call: CommitFence(id=%s, token=%s)", id, token)
}

func (c *mockCache[E]) ReleaseFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	if c.ReleaseFenceFn != nil {
		return c.ReleaseFenceFn(ctx, id, version, token)
	}

	return fmt.Errorf("unexpected call: ReleaseFence(id=%s, token=%s)", id, token)
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
		name          string
		haveInner     func() aggregatestore.Store[mockEntity]
		haveCache     func() aggregatestore.AggregateCache[mockEntity]
		haveOpts      *aggregatestore.SaveOptions
		haveAggregate func() *aggregatestore.Aggregate[mockEntity]
		wantAggregate *aggregatestore.Aggregate[mockEntity]
		wantErr       error
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
					ReserveFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
						return nil
					},
					CommitFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
						return nil
					},
					PutAggregateFn: func(context.Context, typeid.ID, aggregatestore.CachedAggregate[mockEntity]) error {
						return nil
					},
				}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				aggregate := newMockAggregate(aggregateID, 0)
				aggregate.Append(mockEntityEventA{})
				return aggregate
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "delegates a save with no queued events and leaves the cache untouched",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					SaveFn: func(context.Context, *aggregatestore.Aggregate[mockEntity], *aggregatestore.SaveOptions) error {
						return nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				// The bare mock refuses every cache call: a fence would refuse
				// the save, so reaching wantAggregate proves none was raised.
				return &mockCache[mockEntity]{}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			// An unmarked inner error vouches for no outcome, so no settlement
			// runs: the bare Commit/Release mocks would error if called, and the
			// warn-only settle path would hide it, but the reservation standing
			// is pinned behaviorally by TestCachedStore_AmbiguousSaveOutcomeKeepsTheFence.
			name: "returns an error when the inner store fails without vouching for an outcome",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					SaveFn: func(context.Context, *aggregatestore.Aggregate[mockEntity], *aggregatestore.SaveOptions) error {
						return errors.New("mock error")
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					ReserveFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
						return nil
					},
				}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				aggregate := newMockAggregate(aggregateID, 42)
				aggregate.Append(mockEntityEventA{})
				return aggregate
			},
			wantErr: errors.New("saving to inner aggregate store: mock error"),
		},
		{
			name: "releases the reservation when the inner store reports nothing appended",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{
					SaveFn: func(context.Context, *aggregatestore.Aggregate[mockEntity], *aggregatestore.SaveOptions) error {
						return fmt.Errorf("mock error: %w", aggregatestore.ErrNoEventsAppended)
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{
					ReserveFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
						return nil
					},
					ReleaseFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
						return nil
					},
				}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				aggregate := newMockAggregate(aggregateID, 42)
				aggregate.Append(mockEntityEventA{})
				return aggregate
			},
			wantErr: errors.New("saving to inner aggregate store: mock error: no events were appended"),
		},
		{
			name: "refuses a save from a negative version before touching the cache",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				// The bare mock refuses every cache call: any fence would fail
				// the save with a fencing error, not the validation error.
				return &mockCache[mockEntity]{}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				aggregate := newMockAggregate(aggregateID, -1)
				aggregate.Append(mockEntityEventA{})
				return aggregate
			},
			wantErr: errors.New("validating aggregate version: aggregate version -1 is invalid"),
		},
		{
			name: "refuses a save that would overflow the version space before touching the cache",
			haveInner: func() aggregatestore.Store[mockEntity] {
				return &mockAggregateStore[mockEntity]{}
			},
			haveCache: func() aggregatestore.AggregateCache[mockEntity] {
				return &mockCache[mockEntity]{}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				aggregate := newMockAggregate(aggregateID, math.MaxInt64)
				aggregate.Append(mockEntityEventA{})
				return aggregate
			},
			wantErr: errors.New("validating aggregate version: cannot append 1 events at version 9223372036854775807: aggregate versions end at 9223372036854775807"),
		},
		{
			name: "returns an error when the inner store refuses a no-event save",
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
				return newMockAggregate(aggregateID, 0)
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
					ReserveFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
						return nil
					},
					CommitFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
						return nil
					},
					PutAggregateFn: func(context.Context, typeid.ID, aggregatestore.CachedAggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				}
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				aggregate := newMockAggregate(aggregateID, 0)
				aggregate.Append(mockEntityEventA{})
				return aggregate
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

func (c *gatedPutCache[S]) ReserveFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	return c.inner.ReserveFence(ctx, id, version, token)
}

func (c *gatedPutCache[S]) CommitFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	return c.inner.CommitFence(ctx, id, version, token)
}

func (c *gatedPutCache[S]) ReleaseFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	return c.inner.ReleaseFence(ctx, id, version, token)
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
		inner:       aggregatestore.NewMemoryAggregateCache[account](),
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

	// The backend refused the parked write outright, so the regressed entry
	// never landed and the backend still holds version 3.
	entry, err := gated.inner.GetAggregate(t.Context(), loaded.ID())
	if err != nil || entry == nil {
		t.Fatalf("reading the backing cache entry: %+v, %v", entry, err)
	}

	if entry.Version != 3 {
		t.Errorf("want the backing entry still at version 3, got %d", entry.Version)
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
// the load's publication runs last, carrying the older version, and the
// backend must drop it.
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

	cached, err := aggregatestore.NewCachedStore(aggregatestore.Store[account](gatedInner), aggregatestore.NewMemoryAggregateCache[account]())
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

// TestCachedStore_SharedBackendCannotBeRegressedAcrossStores pins publication
// ordering as a property of the shared backend, not of any one store: two
// CachedStores over one backend publish out of order, and no store sees both
// writes. W1 misses and parks its version-2 publication; W2 persists and
// publishes version 3; the released write lands last and the backend must
// drop it. The publishing store and a store with no history alike must then
// serve version 3, and the backend entry itself must still hold it.
func TestCachedStore_SharedBackendCannotBeRegressedAcrossStores(t *testing.T) {
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

	shared := aggregatestore.NewMemoryAggregateCache[account]()
	gated := &gatedPutCache[account]{
		inner:       shared,
		armed:       true,
		gateVersion: 2,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}

	w1, err := aggregatestore.NewCachedStore[account](base, gated)
	if err != nil {
		t.Fatalf("creating cached store w1: %v", err)
	}

	w2, err := aggregatestore.NewCachedStore[account](base, shared)
	if err != nil {
		t.Fatalf("creating cached store w2: %v", err)
	}

	// Version 2 exists durably before either store's cache sees anything.
	id := uuid.Must(uuid.NewV4())
	seed := base.New(id)
	seed.Append(fundsDeposited{Amount: 10}, fundsDeposited{Amount: 5})
	if err := base.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// W1 misses, reads version 2, and parks inside its put to the shared backend.
	read := make(chan error, 1)
	go func() {
		_, err := w1.Load(context.Background(), id, nil)
		read <- err
	}()

	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for w1's cache write to park")
	}

	// W2 persists and publishes version 3 to the shared backend.
	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("w2 load: %v", err)
	}

	aggregate.Append(fundsWithdrawn{Amount: 3})
	if err := w2.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("w2 save: %v", err)
	}

	// Release W1's parked put: version 2 lands after version 3.
	close(gated.release)
	if err := <-read; err != nil {
		t.Fatalf("w1 load: %v", err)
	}

	loaded, err := w1.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through w1: %v", err)
	}

	if loaded.Version() != 3 {
		t.Errorf("w1: want the cache serving version 3, got %d (the shared backend regressed)", loaded.Version())
	}

	// A store with no publication history over the same backend must not
	// serve a regressed entry either.
	w3, err := aggregatestore.NewCachedStore[account](base, shared)
	if err != nil {
		t.Fatalf("creating cached store w3: %v", err)
	}

	loaded3, err := w3.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through w3: %v", err)
	}

	if loaded3.Version() != 3 {
		t.Errorf("w3: want the cache serving version 3, got %d", loaded3.Version())
	}

	entry, err := shared.GetAggregate(t.Context(), loaded.ID())
	if err != nil || entry == nil {
		t.Fatalf("reading the backing cache entry: %+v, %v", entry, err)
	}

	if entry.Version != 3 {
		t.Errorf("want the backing entry still at version 3, got %d", entry.Version)
	}
}

// TestCachedStore_SkipApplySaveFencesTheCache pins the durable advance a
// SkipApply save makes without leaving a publishable state: the stream moves,
// the aggregate's state and version stay behind, and the old cache entry is
// now stale. The save must fence the cache at the newest appended version —
// any lower, and a racing publication below the durable tip would still land
// and be served.
func TestCachedStore_SkipApplySaveFencesTheCache(t *testing.T) {
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

	cache := aggregatestore.NewMemoryAggregateCache[account]()

	cached, err := aggregatestore.NewCachedStore[account](base, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seed := cached.New(id)
	seed.Append(fundsDeposited{Amount: 10})
	if err := cached.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// The skip-apply save appends two events: versions 2 and 3 become durable
	// while the aggregate stays at version 1.
	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5}, fundsDeposited{Amount: 7})
	if err := cached.Save(t.Context(), aggregate, &aggregatestore.SaveOptions{SkipApply: true}); err != nil {
		t.Fatalf("skip-apply save: %v", err)
	}

	// A racing publication below the durable tip must be refused by the
	// fence: version 2 is as stale as version 1 once version 3 is a fact.
	if err := cache.PutAggregate(t.Context(), seed.ID(), aggregatestore.CachedAggregate[account]{
		State:   account{Balance: 999},
		Version: 2,
	}); err != nil {
		t.Fatalf("racing put: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 3 {
		t.Errorf("want version 3 after the durable advance, got %d (stale entry served)", loaded.Version())
	}

	if balance := loaded.State().Balance; balance != 22 {
		t.Errorf("want the durable state (balance 22), got %d", balance)
	}

	// The load repopulated the backend at the durable tip, state and all.
	entry, err := cache.GetAggregate(t.Context(), seed.ID())
	if err != nil || entry == nil {
		t.Fatalf("reading the backing cache entry: %+v, %v", entry, err)
	}

	if entry.Version != 3 || entry.State.Balance != 22 {
		t.Errorf("want the backing entry repopulated with the durable payload (version 3, balance 22), got version %d balance %d",
			entry.Version, entry.State.Balance)
	}

	// A hit serves exactly what was published.
	hit, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading the published entry: %v", err)
	}

	if hit.Version() != 3 || hit.State().Balance != 22 {
		t.Errorf("want the hit serving the published entry (version 3, balance 22), got version %d balance %d",
			hit.Version(), hit.State().Balance)
	}
}

// postAppendFailStore forces the faithful ErrEventsAppended shape: the inner
// save appends durably but applies nothing (skip-apply), and the returned
// error reports the events as appended but unapplied.
type postAppendFailStore struct {
	aggregatestore.Store[account]
	armed bool
}

func (s *postAppendFailStore) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[account], opts *aggregatestore.SaveOptions) error {
	if !s.armed {
		return s.Store.Save(ctx, aggregate, opts)
	}
	s.armed = false

	forced := aggregatestore.SaveOptions{SkipApply: true}
	if err := s.Store.Save(ctx, aggregate, &forced); err != nil {
		return err
	}

	return fmt.Errorf("applying aggregate event: %w", aggregatestore.ErrEventsAppended)
}

// TestCachedStore_PostAppendFailureFencesTheCache pins the recovery contract
// ErrEventsAppended documents: the events are durable facts, the caller
// discards the aggregate and reloads, and the reload replays them. The cached
// entry predates the append, so the failing save must fence it out before the
// error surfaces; the fence stands at the durable tip, which outranks every
// entry the append outdated without blocking the reload's republication.
func TestCachedStore_PostAppendFailureFencesTheCache(t *testing.T) {
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

	failing := &postAppendFailStore{Store: base}
	cache := aggregatestore.NewMemoryAggregateCache[account]()

	cached, err := aggregatestore.NewCachedStore[account](failing, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seed := cached.New(id)
	seed.Append(fundsDeposited{Amount: 10})
	if err := cached.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	failing.armed = true

	err = cached.Save(t.Context(), aggregate, nil)
	if !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want a save error carrying ErrEventsAppended, got %v", err)
	}

	// The documented recovery: discard the aggregate and reload it.
	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("reloading through the cache: %v", err)
	}

	if loaded.Version() != 2 {
		t.Errorf("want the reload replaying the appended events (version 2), got %d (stale entry served)", loaded.Version())
	}

	if balance := loaded.State().Balance; balance != 15 {
		t.Errorf("want the durable state (balance 15), got %d", balance)
	}

	// The reload republished at the durable tip: the fence admitted it.
	entry, err := cache.GetAggregate(t.Context(), seed.ID())
	if err != nil || entry == nil {
		t.Fatalf("reading the backing cache entry: %+v, %v", entry, err)
	}

	if entry.Version != 2 {
		t.Errorf("want the backing entry republished at version 2, got %d", entry.Version)
	}
}

// flakyCache delegates to an inner cache and fails operations on command,
// for pinning the save protocol's behavior when the cache misbehaves.
type flakyCache[S any] struct {
	aggregatestore.AggregateCache[S]
	mu         sync.Mutex
	failPuts   bool
	failFences bool
	fenceSkips int
	fenceCalls int
}

func (c *flakyCache[S]) PutAggregate(ctx context.Context, id typeid.ID, entry aggregatestore.CachedAggregate[S]) error {
	c.mu.Lock()
	fail := c.failPuts
	c.mu.Unlock()

	if fail {
		return errors.New("cache put refused")
	}

	return c.AggregateCache.PutAggregate(ctx, id, entry)
}

func (c *flakyCache[S]) ReserveFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	c.mu.Lock()
	n := c.fenceCalls
	c.fenceCalls++
	fail := c.failFences && n >= c.fenceSkips
	c.mu.Unlock()

	if fail {
		return errors.New("cache fence refused")
	}

	return c.AggregateCache.ReserveFence(ctx, id, version, token)
}

// armFenceFailures makes fence calls fail after the next afterCalls calls
// succeed, counting from now.
func (c *flakyCache[S]) armFenceFailures(afterCalls int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failFences = true
	c.fenceSkips = afterCalls
	c.fenceCalls = 0
}

func (c *flakyCache[S]) armPutFailures() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failPuts = true
}

func (c *flakyCache[S]) disarm() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failPuts = false
	c.failFences = false
}

func newFlakyFixture(t *testing.T) (aggregatestore.Store[account], *aggregatestore.CachedStore[account], *flakyCache[account], uuid.UUID) {
	t.Helper()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	base, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}, fundsWithdrawn{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	flaky := &flakyCache[account]{AggregateCache: aggregatestore.NewMemoryAggregateCache[account]()}

	cached, err := aggregatestore.NewCachedStore[account](base, flaky)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	return base, cached, flaky, uuid.Must(uuid.NewV4())
}

func seedOne(t *testing.T, cached *aggregatestore.CachedStore[account], id uuid.UUID) {
	t.Helper()

	seed := cached.New(id)
	seed.Append(fundsDeposited{Amount: 10})
	if err := cached.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}
}

// TestCachedStore_SaveRefusedWhenTheCacheCannotBeFenced pins the fail-closed
// half of the save protocol: the pre-append fence is the one cache write
// whose failure must refuse the save, because everything after the append is
// unenforceable. Nothing is appended, the events stay queued, and a retry
// once the cache recovers completes the save.
func TestCachedStore_SaveRefusedWhenTheCacheCannotBeFenced(t *testing.T) {
	t.Parallel()

	base, cached, flaky, id := newFlakyFixture(t)

	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	flaky.armFenceFailures(0)

	if err := cached.Save(t.Context(), aggregate, nil); err == nil {
		t.Fatal("want the save refused when the cache cannot be fenced, got nil")
	}

	// Nothing was appended: the stream still ends at the seed.
	durable, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading durably: %v", err)
	}

	if durable.Version() != 1 {
		t.Fatalf("want nothing appended by the refused save, got version %d", durable.Version())
	}

	// The events stayed queued: the same save succeeds once the cache recovers.
	flaky.disarm()

	if err := cached.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("retrying the save: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 2 || loaded.State().Balance != 15 {
		t.Errorf("want the retried save served (version 2, balance 15), got version %d balance %d",
			loaded.Version(), loaded.State().Balance)
	}
}

// TestCachedStore_PublicationFailureCannotServeTheOutdatedEntry pins the
// fail-open half: once the pre-append fence stands, a failed publication
// costs a miss, never a stale hit — the entry the append outdated was
// already evicted, so the load falls through to the inner store.
func TestCachedStore_PublicationFailureCannotServeTheOutdatedEntry(t *testing.T) {
	t.Parallel()

	base, cached, flaky, id := newFlakyFixture(t)

	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	flaky.armPutFailures()

	if err := cached.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 2 || loaded.State().Balance != 15 {
		t.Errorf("want the durable state served despite the failed publication (version 2, balance 15), got version %d balance %d",
			loaded.Version(), loaded.State().Balance)
	}
}

// TestCachedStore_LostPublicationCannotAdmitAnIntermediateVersion pins where
// the pre-append fence stands: at the expected durable tip, not one past the
// starting version. A two-event save that loses its publication leaves the
// fence as the only guard, and a racing publication of the intermediate
// version — equal to any lesser fence — must still be refused.
func TestCachedStore_LostPublicationCannotAdmitAnIntermediateVersion(t *testing.T) {
	t.Parallel()

	base, cached, flaky, id := newFlakyFixture(t)

	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	aggregate.Append(fundsDeposited{Amount: 7})
	flaky.armPutFailures()

	// Durable version 3; the publication of version 3 is lost.
	if err := cached.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("two-event save: %v", err)
	}

	flaky.disarm()

	// A racing publication of the intermediate version arrives at the backend.
	if err := flaky.AggregateCache.PutAggregate(t.Context(), aggregate.ID(), aggregatestore.CachedAggregate[account]{
		State:   account{Balance: 15},
		Version: 2,
	}); err != nil {
		t.Fatalf("racing put: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 3 || loaded.State().Balance != 22 {
		t.Errorf("want the durable tip served (version 3, balance 22), got version %d balance %d (intermediate version admitted)",
			loaded.Version(), loaded.State().Balance)
	}

	entry, err := flaky.GetAggregate(t.Context(), aggregate.ID())
	if err != nil || entry == nil {
		t.Fatalf("reading the backing cache entry: %+v, %v", entry, err)
	}

	if entry.Version != 3 || entry.State.Balance != 22 {
		t.Errorf("want the backing entry at the durable tip (version 3, balance 22), got version %d balance %d",
			entry.Version, entry.State.Balance)
	}
}

// TestCachedStore_FailedSaveCannotPoisonTheFloor pins the abort half of the
// reservation protocol: a save refused by optimistic concurrency appended
// nothing, so its released reservation must not keep outlawing the versions
// the stream actually holds — caching resumes at durable truth instead of
// waiting for the stream to reach a tip the failed batch never wrote.
func TestCachedStore_FailedSaveCannotPoisonTheFloor(t *testing.T) {
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

	mem := aggregatestore.NewMemoryAggregateCache[account]()

	cached, err := aggregatestore.NewCachedStore[account](base, mem)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	// Two commands load version 1.
	stale, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading (stale): %v", err)
	}

	winner, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading (winner): %v", err)
	}

	// The winner saves one event and publishes valid version 2.
	winner.Append(fundsDeposited{Amount: 5})
	if err := cached.Save(t.Context(), winner, nil); err != nil {
		t.Fatalf("winner save: %v", err)
	}

	// The stale command queues two events, reserves a fence at version 3,
	// and fails optimistic concurrency without appending.
	stale.Append(fundsDeposited{Amount: 100})
	stale.Append(fundsDeposited{Amount: 200})
	if err := cached.Save(t.Context(), stale, nil); err == nil {
		t.Fatal("want the stale save refused by optimistic concurrency, got nil")
	}

	durable, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading durably: %v", err)
	}

	if durable.Version() != 2 {
		t.Fatalf("want the stream at version 2, got %d", durable.Version())
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 2 || loaded.State().Balance != 15 {
		t.Fatalf("want version 2 balance 15 served, got version %d balance %d",
			loaded.Version(), loaded.State().Balance)
	}

	entry, err := mem.GetAggregate(t.Context(), loaded.ID())
	if err != nil || entry == nil {
		t.Fatalf("want the version-2 republication admitted after the failed save, got %+v, %v (floor poisoned)", entry, err)
	}

	if entry.Version != 2 || entry.State.Balance != 15 {
		t.Errorf("want the backing entry at version 2 balance 15, got version %d balance %d",
			entry.Version, entry.State.Balance)
	}
}

// ambiguousSaveStore delegates a save and, when armed, discards its success:
// the append is durable but the returned error vouches for no outcome, the
// shape of a writer that committed and lost its response.
type ambiguousSaveStore struct {
	aggregatestore.Store[account]
	armed bool
}

func (s *ambiguousSaveStore) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[account], opts *aggregatestore.SaveOptions) error {
	if !s.armed {
		return s.Store.Save(ctx, aggregate, opts)
	}
	s.armed = false

	if err := s.Store.Save(ctx, aggregate, opts); err != nil {
		return err
	}

	return errors.New("append response lost")
}

// TestCachedStore_AmbiguousSaveOutcomeKeepsTheFence pins the third save
// outcome: an inner error carrying neither ErrEventsAppended nor
// ErrNoEventsAppended vouches for nothing, so the reservation must stand.
// Here the append is durable — releasing on the unmarked error would admit
// a delayed publication of the outdated version and serve it indefinitely.
func TestCachedStore_AmbiguousSaveOutcomeKeepsTheFence(t *testing.T) {
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

	ambiguous := &ambiguousSaveStore{Store: base}
	cache := aggregatestore.NewMemoryAggregateCache[account]()

	cached, err := aggregatestore.NewCachedStore[account](ambiguous, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	ambiguous.armed = true

	saveErr := cached.Save(t.Context(), aggregate, nil)
	if saveErr == nil {
		t.Fatal("want the ambiguous save surfacing an error, got nil")
	}
	if errors.Is(saveErr, aggregatestore.ErrEventsAppended) || errors.Is(saveErr, aggregatestore.ErrNoEventsAppended) {
		t.Fatalf("want an error vouching for no outcome, got %v", saveErr)
	}

	// A delayed publication of the outdated version must be refused by the
	// standing reservation.
	if err := cache.PutAggregate(t.Context(), aggregate.ID(), aggregatestore.CachedAggregate[account]{
		State:   account{Balance: 10},
		Version: 1,
	}); err != nil {
		t.Fatalf("delayed put: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 2 || loaded.State().Balance != 15 {
		t.Errorf("want durable version 2 balance 15 served, got version %d balance %d (the unmarked failure unfenced the append)",
			loaded.Version(), loaded.State().Balance)
	}

	// The load's republication at the reserved tip was admitted: standing at
	// the floor over-blocks below it, never above.
	entry, err := cache.GetAggregate(t.Context(), loaded.ID())
	if err != nil || entry == nil {
		t.Fatalf("reading the backing cache entry: %+v, %v", entry, err)
	}

	if entry.Version != 2 || entry.State.Balance != 15 {
		t.Errorf("want the backing entry repopulated at version 2 balance 15, got version %d balance %d",
			entry.Version, entry.State.Balance)
	}
}

// TestCachedStore_PostSaveHookFailureKeepsTheAppendFenced pins the decorator
// half of the outcome contract: a post-save hook error follows a save that
// succeeded, HookableStore marks it ErrEventsAppended, and the fence commits
// — a delayed publication of the outdated version stays refused.
func TestCachedStore_PostSaveHookFailureKeepsTheAppendFenced(t *testing.T) {
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

	hookable, err := aggregatestore.NewHookableStore[account](base)
	if err != nil {
		t.Fatalf("creating hookable store: %v", err)
	}

	armed := false
	hookable.AfterSave(func(context.Context, *aggregatestore.Aggregate[account]) error {
		if !armed {
			return nil
		}
		armed = false
		return errors.New("post-save side effect failed")
	})

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	cached, err := aggregatestore.NewCachedStore[account](hookable, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	armed = true

	saveErr := cached.Save(t.Context(), aggregate, nil)
	if !errors.Is(saveErr, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want the post-save hook failure carrying ErrEventsAppended, got %v", saveErr)
	}

	if err := cache.PutAggregate(t.Context(), aggregate.ID(), aggregatestore.CachedAggregate[account]{
		State:   account{Balance: 10},
		Version: 1,
	}); err != nil {
		t.Fatalf("delayed put: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 2 || loaded.State().Balance != 15 {
		t.Errorf("want durable version 2 balance 15 served, got version %d balance %d (the post-save hook error unfenced the append)",
			loaded.Version(), loaded.State().Balance)
	}
}

// ambiguousReserveCache delegates a reservation and, when armed, discards
// its success: the reservation takes effect at the backend but the caller
// sees only an error.
type ambiguousReserveCache[S any] struct {
	aggregatestore.AggregateCache[S]
	armed bool
}

func (c *ambiguousReserveCache[S]) ReserveFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	if !c.armed {
		return c.AggregateCache.ReserveFence(ctx, id, version, token)
	}
	c.armed = false

	if err := c.AggregateCache.ReserveFence(ctx, id, version, token); err != nil {
		return err
	}

	return errors.New("reserve response lost")
}

// TestCachedStore_RefusedReservationIsWithdrawn pins the cleanup half of
// caller-minted tokens: a reservation whose reserve call errs may still have
// taken effect, so the refused save withdraws it by token — the cache resumes
// serving durable truth instead of over-blocking on a reservation nobody
// will ever settle.
func TestCachedStore_RefusedReservationIsWithdrawn(t *testing.T) {
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

	mem := aggregatestore.NewMemoryAggregateCache[account]()
	wrapper := &ambiguousReserveCache[account]{AggregateCache: mem}

	cached, err := aggregatestore.NewCachedStore[account](base, wrapper)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	wrapper.armed = true

	saveErr := cached.Save(t.Context(), aggregate, nil)
	if saveErr == nil {
		t.Fatal("want the save refused when the reservation errors, got nil")
	}
	if !errors.Is(saveErr, aggregatestore.ErrNoEventsAppended) {
		t.Fatalf("want the refused save reporting nothing appended, got %v", saveErr)
	}

	// Durable truth is still version 1, and the withdrawn reservation must
	// let the miss republish it.
	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}
	if loaded.Version() != 1 {
		t.Fatalf("want durable version 1 served, got %d", loaded.Version())
	}

	entry, err := mem.GetAggregate(t.Context(), loaded.ID())
	if err != nil || entry == nil {
		t.Fatalf("want the version-1 republication admitted after the refused save, got %+v, %v (the errored reservation leaked)", entry, err)
	}

	if entry.Version != 1 {
		t.Errorf("want the backing entry at version 1, got %d", entry.Version)
	}
}

// ctxBoundReleaseCache refuses a release whose context is already dead,
// modeling a backend that honors cancellation.
type ctxBoundReleaseCache[S any] struct {
	aggregatestore.AggregateCache[S]
}

func (c *ctxBoundReleaseCache[S]) ReleaseFence(ctx context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return c.AggregateCache.ReleaseFence(ctx, id, version, token)
}

// TestCachedStore_CanceledCallerCannotLeakTheReservation pins fence
// settlement's independence from the caller's context: a save canceled
// mid-flight still releases the reservation its refused append no longer
// needs, so a later successful save can be cached — the leak would otherwise
// hold the floor at the failed save's tip forever.
func TestCachedStore_CanceledCallerCannotLeakTheReservation(t *testing.T) {
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

	mem := aggregatestore.NewMemoryAggregateCache[account]()
	wrapper := &ctxBoundReleaseCache[account]{AggregateCache: mem}

	cached, err := aggregatestore.NewCachedStore[account](base, wrapper)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	// A stale command queues 100 events from version 1: expected tip 101.
	stale, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading (stale): %v", err)
	}
	for range 100 {
		stale.Append(fundsDeposited{Amount: 1})
	}

	winner, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading (winner): %v", err)
	}
	winner.Append(fundsDeposited{Amount: 5})
	if err := cached.Save(t.Context(), winner, nil); err != nil {
		t.Fatalf("winner save: %v", err)
	}

	// The stale save runs under a dead context: it reserves at 101, fails
	// optimistic concurrency, and its release must not die with the caller.
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if err := cached.Save(canceled, stale, nil); err == nil {
		t.Fatal("want the stale save refused by optimistic concurrency, got nil")
	}

	third, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading (third): %v", err)
	}
	third.Append(fundsDeposited{Amount: 2})
	if err := cached.Save(t.Context(), third, nil); err != nil {
		t.Fatalf("third save: %v", err)
	}

	entry, err := mem.GetAggregate(t.Context(), third.ID())
	if err != nil || entry == nil {
		t.Fatalf("want the version-3 save cached, got %+v, %v (the canceled release leaked the reservation)", entry, err)
	}

	if entry.Version != 3 || entry.State.Balance != 17 {
		t.Errorf("want the backing entry at version 3 balance 17, got version %d balance %d",
			entry.Version, entry.State.Balance)
	}
}

// TestCachedStore_FenceSettlementIsDetachedAndBounded pins the contexts the
// save protocol hands the cache: settlement gets one detached from the
// caller's cancellation and bounded by a deadline, while publication stays
// on the caller's own context — losing a put to cancellation costs a miss,
// losing a settlement would leak a reservation.
func TestCachedStore_FenceSettlementIsDetachedAndBounded(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	// The contexts are sampled inside the calls: settlement cancels its own
	// bounded context once the call returns.
	var commitCalled, commitBounded bool
	var commitCtxErr, putCtxErr error
	putCalled := false
	cache := &mockCache[mockEntity]{
		ReserveFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
			return nil
		},
		CommitFenceFn: func(ctx context.Context, _ typeid.ID, _ int64, _ aggregatestore.FenceToken) error {
			commitCalled = true
			commitCtxErr = ctx.Err()
			_, commitBounded = ctx.Deadline()
			return nil
		},
		PutAggregateFn: func(ctx context.Context, _ typeid.ID, _ aggregatestore.CachedAggregate[mockEntity]) error {
			putCalled = true
			putCtxErr = ctx.Err()
			return nil
		},
	}

	inner := &mockAggregateStore[mockEntity]{
		SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
			aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), 42)
			return nil
		},
	}

	store, err := aggregatestore.NewCachedStore[mockEntity](inner, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	aggregate := newMockAggregate(aggregateID, 0)
	aggregate.Append(mockEntityEventA{})

	if err := store.Save(canceled, aggregate, nil); err != nil {
		t.Fatalf("saving: %v", err)
	}

	if !commitCalled {
		t.Fatal("want the fence committed, got no CommitFence call")
	}
	if commitCtxErr != nil {
		t.Errorf("want settlement detached from the canceled caller, got context error %v", commitCtxErr)
	}
	if !commitBounded {
		t.Error("want settlement bounded by a deadline, got none")
	}

	if !putCalled {
		t.Fatal("want the publication attempted, got no PutAggregate call")
	}
	if putCtxErr == nil {
		t.Error("want publication on the caller's own context, got a detached one")
	}
}

// TestCachedStore_MintsAUniqueFenceTokenPerSave pins token uniqueness, which
// the release-by-token generation check depends on: two saves must never
// share a token, or settling one save's reservation could settle the other's.
func TestCachedStore_MintsAUniqueFenceTokenPerSave(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	var tokens []aggregatestore.FenceToken
	cache := &mockCache[mockEntity]{
		ReserveFenceFn: func(_ context.Context, _ typeid.ID, _ int64, token aggregatestore.FenceToken) error {
			tokens = append(tokens, token)
			return nil
		},
		CommitFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
			return nil
		},
		PutAggregateFn: func(context.Context, typeid.ID, aggregatestore.CachedAggregate[mockEntity]) error {
			return nil
		},
	}

	inner := &mockAggregateStore[mockEntity]{
		SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
			aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), aggregate.Version()+1)
			return nil
		},
	}

	store, err := aggregatestore.NewCachedStore[mockEntity](inner, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	for range 2 {
		aggregate := newMockAggregate(aggregateID, 0)
		aggregate.Append(mockEntityEventA{})
		if err := store.Save(t.Context(), aggregate, nil); err != nil {
			t.Fatalf("saving: %v", err)
		}
	}

	if len(tokens) != 2 {
		t.Fatalf("want 2 reservations, got %d", len(tokens))
	}

	if tokens[0] == "" || tokens[1] == "" {
		t.Errorf("want non-empty tokens, got %q and %q", tokens[0], tokens[1])
	}

	if tokens[0] == tokens[1] {
		t.Errorf("want each save minting its own token, got %q twice", tokens[0])
	}
}

// TestCachedStore_PostAppendPublicationFailureCannotServeTheOutdatedEntry
// pins the multi-event ErrEventsAppended path with every later cache write
// failing and a racing intermediate publication arriving: the pre-append
// fence alone, standing at the durable tip, must keep both outdated
// versions from the documented discard-and-reload recovery.
func TestCachedStore_PostAppendPublicationFailureCannotServeTheOutdatedEntry(t *testing.T) {
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

	failing := &postAppendFailStore{Store: base}
	flaky := &flakyCache[account]{AggregateCache: aggregatestore.NewMemoryAggregateCache[account]()}

	cached, err := aggregatestore.NewCachedStore[account](failing, flaky)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	aggregate.Append(fundsDeposited{Amount: 7})
	failing.armed = true
	flaky.armPutFailures()

	if err := cached.Save(t.Context(), aggregate, nil); !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want a save error carrying ErrEventsAppended, got %v", err)
	}

	// A racing publication of the intermediate version arrives at the backend.
	if err := flaky.AggregateCache.PutAggregate(t.Context(), aggregate.ID(), aggregatestore.CachedAggregate[account]{
		State:   account{Balance: 15},
		Version: 2,
	}); err != nil {
		t.Fatalf("racing put: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("reloading through the cache: %v", err)
	}

	if loaded.Version() != 3 || loaded.State().Balance != 22 {
		t.Errorf("want the reload replaying the appended events (version 3, balance 22), got version %d balance %d (stale entry served)",
			loaded.Version(), loaded.State().Balance)
	}
}

// parkedSaveStore delegates a save to the inner store and, when armed, parks
// after the commit and before returning — holding the caller's post-save
// cache work open while the appended events are already facts.
type parkedSaveStore struct {
	aggregatestore.Store[account]
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (s *parkedSaveStore) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[account], opts *aggregatestore.SaveOptions) error {
	err := s.Store.Save(ctx, aggregate, opts)

	s.mu.Lock()
	gate := s.armed
	s.armed = false
	s.mu.Unlock()

	if gate {
		close(s.entered)
		<-s.release
	}

	return err
}

// TestCachedStore_SaveWindowCannotServeTheOutdatedEntry pins the window
// between the append and its publication: the committed events are already
// visible to a versioned read, so an unversioned load in that window must
// not serve the entry the commit outdated — the pre-append fence evicted it
// before the events could become facts.
func TestCachedStore_SaveWindowCannotServeTheOutdatedEntry(t *testing.T) {
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

	parked := &parkedSaveStore{
		Store:   base,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}

	cached, err := aggregatestore.NewCachedStore[account](aggregatestore.Store[account](parked), aggregatestore.NewMemoryAggregateCache[account]())
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	parked.mu.Lock()
	parked.armed = true
	parked.mu.Unlock()

	saved := make(chan error, 1)
	go func() {
		saved <- cached.Save(context.Background(), aggregate, nil)
	}()

	select {
	case <-parked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the save to park")
	}

	// The commit is a fact: a versioned load reads it from the stream.
	versioned, err := cached.Load(t.Context(), id, &aggregatestore.LoadOptions{ToVersion: 2})
	if err != nil {
		t.Fatalf("versioned load: %v", err)
	}

	if versioned.Version() != 2 {
		t.Fatalf("want the versioned load reading committed version 2, got %d", versioned.Version())
	}

	// The unversioned load must not serve the entry the commit outdated.
	loaded, err := cached.Load(t.Context(), id, nil)

	close(parked.release)
	if err != nil {
		t.Fatalf("unversioned load: %v", err)
	}

	if err := <-saved; err != nil {
		t.Fatalf("save: %v", err)
	}

	if loaded.Version() != 2 {
		t.Errorf("want the unversioned load serving committed version 2, got %d (outdated entry served)", loaded.Version())
	}
}

// ptrCounter is reference-bearing state whose ApplyTo mutates in place, as
// the package supports elsewhere. It pins the cache's detachment contract.
type ptrCounter struct{ Balance int }

type ptrBumped struct {
	Amount int
}

func (ptrBumped) EventType() string { return "ptrbumped" }

func (ptrBumped) New() estoria.DomainEvent[*ptrCounter] { return &ptrBumped{} }

func (e ptrBumped) ApplyTo(c *ptrCounter) *ptrCounter {
	if c == nil {
		c = &ptrCounter{}
	}
	c.Balance += e.Amount

	return c
}

// TestCachedStore_ServesDetachedState pins the detachment contract through
// the whole composition: two aggregates loaded from one cache entry must not
// share memory with each other or with the entry, so a save through one —
// applying events in place — cannot mutate the other's history.
func TestCachedStore_ServesDetachedState(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	base, err := aggregatestore.New(eventStore, "ptrcounter",
		func(uuid.UUID) *ptrCounter { return &ptrCounter{} },
		aggregatestore.WithEventTypes[*ptrCounter](ptrBumped{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	cached, err := aggregatestore.NewCachedStore[*ptrCounter](base, aggregatestore.NewMemoryAggregateCache[*ptrCounter]())
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seed := cached.New(id)
	seed.Append(ptrBumped{Amount: 10})
	if err := cached.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	first, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	second, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}

	first.Append(ptrBumped{Amount: 5})
	if err := cached.Save(t.Context(), first, nil); err != nil {
		t.Fatalf("saving through the first aggregate: %v", err)
	}

	if second.Version() != 1 {
		t.Fatalf("want the second aggregate still at version 1, got %d", second.Version())
	}

	if got := second.State().Balance; got != 10 {
		t.Errorf("want the version-1 state unchanged (balance 10), got %d (state aliased)", got)
	}
}

// TestCachedStore_NoEventSaveLeavesTheCacheUntouched pins that a save with
// no queued events publishes nothing: the inner no-op validates neither
// state nor version, so state the caller mutated without events must never
// become servable.
func TestCachedStore_NoEventSaveLeavesTheCacheUntouched(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	base, err := aggregatestore.New(eventStore, "ptrcounter",
		func(uuid.UUID) *ptrCounter { return &ptrCounter{} },
		aggregatestore.WithEventTypes[*ptrCounter](ptrBumped{}))
	if err != nil {
		t.Fatalf("creating event sourced store: %v", err)
	}

	mem := aggregatestore.NewMemoryAggregateCache[*ptrCounter]()

	cached, err := aggregatestore.NewCachedStore[*ptrCounter](base, mem)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seed := cached.New(id)
	seed.Append(ptrBumped{Amount: 10})
	if err := cached.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	aggregate, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	// The caller mutates its (detached) state and saves without events.
	aggregate.State().Balance = 999

	if err := cached.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("no-event save: %v", err)
	}

	// Untouched means untouched: the still-valid entry survives — neither
	// replaced by the unpersisted state nor evicted by a needless fence.
	entry, err := mem.GetAggregate(t.Context(), aggregate.ID())
	if err != nil || entry == nil {
		t.Fatalf("want the seeded entry still cached, got %+v, %v", entry, err)
	}

	if entry.Version != 1 || entry.State.Balance != 10 {
		t.Errorf("want the entry unchanged (version 1, balance 10), got version %d balance %d",
			entry.Version, entry.State.Balance)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading after the no-event save: %v", err)
	}

	if got := loaded.State().Balance; got != 10 {
		t.Errorf("want the durable state served (balance 10), got %d (unpersisted state published)", got)
	}
}

// TestCachedStore_NoEventSaveCannotRepublishBelowTheFence pins the other
// half of the same rule: with the fence at the durable advance and its
// publication lost, a no-event save of a supported historical version must
// not resurrect that version as the cached view.
func TestCachedStore_NoEventSaveCannotRepublishBelowTheFence(t *testing.T) {
	t.Parallel()

	base, cached, flaky, id := newFlakyFixture(t)

	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	aggregate.Append(fundsDeposited{Amount: 7})
	flaky.armPutFailures()

	// Durable version 3, fence 3, no entry: the publication was lost.
	if err := cached.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("two-event save: %v", err)
	}

	flaky.disarm()

	historical, err := cached.Load(t.Context(), id, &aggregatestore.LoadOptions{ToVersion: 2})
	if err != nil {
		t.Fatalf("versioned load: %v", err)
	}

	if err := cached.Save(t.Context(), historical, nil); err != nil {
		t.Fatalf("no-event save of the historical aggregate: %v", err)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("unversioned load: %v", err)
	}

	if loaded.Version() != 3 || loaded.State().Balance != 22 {
		t.Errorf("want the durable tip served (version 3, balance 22), got version %d balance %d (historical version republished)",
			loaded.Version(), loaded.State().Balance)
	}
}

// TestCachedStore_HookAppendedEventsAreFenced pins the documented composition
// rule for decorators that introduce events: wrapping CachedStore, a
// before-save hook's appended events arrive as queued events, the fence
// decision sees them, and even with every publication failing the entry the
// append outdated is never served.
func TestCachedStore_HookAppendedEventsAreFenced(t *testing.T) {
	t.Parallel()

	base, cached, flaky, id := newFlakyFixture(t)

	hooked, err := aggregatestore.NewHookableStore[account](cached)
	if err != nil {
		t.Fatalf("creating hookable store: %v", err)
	}

	var hookArmed atomic.Bool
	hooked.BeforeSave(func(_ context.Context, aggregate *aggregatestore.Aggregate[account]) error {
		if hookArmed.CompareAndSwap(true, false) {
			aggregate.Append(fundsDeposited{Amount: 5})
		}

		return nil
	})

	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	hookArmed.Store(true)
	flaky.armPutFailures()

	// The caller queues nothing; the hook appends before CachedStore runs.
	if err := hooked.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	durable, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading durably: %v", err)
	}

	if durable.Version() != 2 {
		t.Fatalf("want the hook's event durable at version 2, got %d", durable.Version())
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}

	if loaded.Version() != 2 || loaded.State().Balance != 15 {
		t.Errorf("want version 2 balance 15 served, got version %d balance %d (stale entry outlived the hook's append)",
			loaded.Version(), loaded.State().Balance)
	}
}

// blockingPutCache blocks one armed put until the caller's context is done
// or the test releases it, modeling a backend waiting on I/O.
type blockingPutCache[S any] struct {
	aggregatestore.AggregateCache[S]
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func newBlockingPutCache[S any](t *testing.T) *blockingPutCache[S] {
	t.Helper()

	cache := &blockingPutCache[S]{
		AggregateCache: aggregatestore.NewMemoryAggregateCache[S](),
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	t.Cleanup(func() { close(cache.release) })

	return cache
}

func (c *blockingPutCache[S]) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.armed = true
}

func (c *blockingPutCache[S]) PutAggregate(ctx context.Context, id typeid.ID, entry aggregatestore.CachedAggregate[S]) error {
	c.mu.Lock()
	gate := c.armed
	c.armed = false
	c.mu.Unlock()

	if gate {
		close(c.entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.release:
			return nil
		}
	}

	return c.AggregateCache.PutAggregate(ctx, id, entry)
}

// TestCachedStore_SaveIsNotStrandedByTheCache pins that post-append cache
// work runs on the caller's context: a cache waiting on I/O cannot hold a
// completed save past its caller's cancellation, and the publication lost
// with it costs a miss, never a stale hit.
func TestCachedStore_SaveIsNotStrandedByTheCache(t *testing.T) {
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

	blocking := newBlockingPutCache[account](t)

	cached, err := aggregatestore.NewCachedStore[account](base, blocking)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	aggregate.Append(fundsDeposited{Amount: 5})
	blocking.arm()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	saved := make(chan error, 1)
	go func() {
		saved <- cached.Save(ctx, aggregate, nil)
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the publication to block")
	}

	// The caller gives up; the completed save must return with it.
	cancel()

	select {
	case err := <-saved:
		if err != nil {
			t.Fatalf("save: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("save stranded in its cache publication after the caller canceled")
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading after the canceled publication: %v", err)
	}

	if loaded.Version() != 2 || loaded.State().Balance != 15 {
		t.Errorf("want the durable state served (version 2, balance 15), got version %d balance %d",
			loaded.Version(), loaded.State().Balance)
	}
}

// TestCachedStore_LoadIsNotStrandedByTheCache pins the same property for the
// miss-path publication: a completed load returns with its canceled caller.
func TestCachedStore_LoadIsNotStrandedByTheCache(t *testing.T) {
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

	blocking := newBlockingPutCache[account](t)

	cached, err := aggregatestore.NewCachedStore[account](base, blocking)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seed := base.New(id)
	seed.Append(fundsDeposited{Amount: 10})
	if err := base.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	blocking.arm()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type loadResult struct {
		aggregate *aggregatestore.Aggregate[account]
		err       error
	}

	loaded := make(chan loadResult, 1)
	go func() {
		aggregate, err := cached.Load(ctx, id, nil)
		loaded <- loadResult{aggregate, err}
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the miss publication to block")
	}

	cancel()

	select {
	case result := <-loaded:
		if result.err != nil {
			t.Fatalf("load: %v", result.err)
		}
		if result.aggregate.Version() != 1 {
			t.Errorf("want the loaded aggregate at version 1, got %d", result.aggregate.Version())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("load stranded in its miss publication after the caller canceled")
	}
}

// errForeignMarkerHook is a pre-save hook failure that happens to contain
// ErrEventsAppended — a hook propagating a foreign save's post-append error.
var errForeignMarkerHook = fmt.Errorf("recording audit trail: %w", aggregatestore.ErrEventsAppended)

// TestCachedStore_ForeignMarkerCannotCommitTheFence pins outcome resolution
// against contradictory marker trees: a pre-save hook error containing
// ErrEventsAppended matches both sentinels under errors.Is once HookableStore
// marks it ErrNoEventsAppended, but it must resolve to the outermost marker —
// nothing was appended — so the fence is released, not committed for an
// append that never ran.
func TestCachedStore_ForeignMarkerCannotCommitTheFence(t *testing.T) {
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

	hookable, err := aggregatestore.NewHookableStore[account](base)
	if err != nil {
		t.Fatalf("creating hookable store: %v", err)
	}

	armed := false
	hookable.BeforeSave(func(context.Context, *aggregatestore.Aggregate[account]) error {
		if !armed {
			return nil
		}
		armed = false
		return errForeignMarkerHook
	})

	mem := aggregatestore.NewMemoryAggregateCache[account]()
	cached, err := aggregatestore.NewCachedStore[account](hookable, mem)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	seedOne(t, cached, id)

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	aggregate.Append(fundsDeposited{Amount: 5})
	armed = true

	saveErr := cached.Save(t.Context(), aggregate, nil)
	if saveErr == nil {
		t.Fatal("want the pre-save hook failure surfacing an error, got nil")
	}

	// The tree is contradictory under errors.Is — exactly why deciders must
	// resolve instead.
	if !errors.Is(saveErr, aggregatestore.ErrEventsAppended) || !errors.Is(saveErr, aggregatestore.ErrNoEventsAppended) {
		t.Fatalf("want the contradictory tree this test exists for, got %v", saveErr)
	}
	if got := aggregatestore.SaveOutcome(saveErr); got != aggregatestore.AppendOutcomeNothingAppended {
		t.Fatalf("want the outermost marker resolved (nothing appended), got %v", got)
	}

	// Nothing was appended: durable truth is version 1, and its
	// republication must be admitted after the released reservation.
	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}
	if loaded.Version() != 1 {
		t.Fatalf("want durable version 1 served, got %d", loaded.Version())
	}

	entry, err := mem.GetAggregate(t.Context(), loaded.ID())
	if err != nil {
		t.Fatalf("reading the backing cache entry: %v", err)
	}
	if entry == nil || entry.Version != 1 {
		t.Errorf("want the version-1 republication admitted after the refused save, got %+v (a fence was committed for an append that never ran)", entry)
	}
}

// delayedReserveCache swallows a reservation: the caller sees an error, the
// backend sees nothing — yet. The request is still in flight and can land
// after the caller has already withdrawn.
type delayedReserveCache[S any] struct {
	aggregatestore.AggregateCache[S]
	reservedID      typeid.ID
	reservedVersion int64
	reservedToken   aggregatestore.FenceToken
}

func (c *delayedReserveCache[S]) ReserveFence(_ context.Context, id typeid.ID, version int64, token aggregatestore.FenceToken) error {
	c.reservedID, c.reservedVersion, c.reservedToken = id, version, token
	return errors.New("reserve response lost")
}

// TestCachedStore_WithdrawnReservationCannotBeResurrected pins the terminal
// half of settlement: the withdrawal of an ambiguously-failed reserve records
// the token settled, so the reserve request landing afterward — a delayed
// delivery — is refused instead of placing a reservation nobody will ever
// settle.
func TestCachedStore_WithdrawnReservationCannotBeResurrected(t *testing.T) {
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

	mem := aggregatestore.NewMemoryAggregateCache[account]()
	wrapper := &delayedReserveCache[account]{AggregateCache: mem}

	cached, err := aggregatestore.NewCachedStore[account](base, wrapper)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	// Seed durable version 1 without touching the cache.
	id := uuid.Must(uuid.NewV4())
	seed := base.New(id)
	seed.Append(fundsDeposited{Amount: 10})
	if err := base.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	aggregate.Append(fundsDeposited{Amount: 5})

	if saveErr := cached.Save(t.Context(), aggregate, nil); !errors.Is(saveErr, aggregatestore.ErrNoEventsAppended) {
		t.Fatalf("want the save refused with nothing appended, got %v", saveErr)
	}

	// The delayed reserve request lands after the withdrawal completed: the
	// settled reservation must not return to pending.
	lateErr := mem.ReserveFence(t.Context(), wrapper.reservedID, wrapper.reservedVersion, wrapper.reservedToken)
	if !errors.Is(lateErr, aggregatestore.ErrFenceReservationRefused) {
		t.Errorf("want the delayed reserve delivery refused, got %v", lateErr)
	}

	loaded, err := cached.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading through the cache: %v", err)
	}
	if loaded.Version() != 1 {
		t.Fatalf("want durable version 1 served, got %d", loaded.Version())
	}

	entry, err := mem.GetAggregate(t.Context(), loaded.ID())
	if err != nil {
		t.Fatalf("reading the backing cache entry: %v", err)
	}
	if entry == nil || entry.Version != 1 {
		t.Errorf("want the version-1 republication admitted, got %+v (the settled reservation returned to pending)", entry)
	}
}

// collidingTokenCache rewrites every fence token to one fixed token before
// delegating, forcing the documented token/version conflict at the backend.
type collidingTokenCache[S any] struct {
	aggregatestore.AggregateCache[S]
}

const collidingToken = aggregatestore.FenceToken("collision")

func (c *collidingTokenCache[S]) ReserveFence(ctx context.Context, id typeid.ID, version int64, _ aggregatestore.FenceToken) error {
	return c.AggregateCache.ReserveFence(ctx, id, version, collidingToken)
}

func (c *collidingTokenCache[S]) CommitFence(ctx context.Context, id typeid.ID, version int64, _ aggregatestore.FenceToken) error {
	return c.AggregateCache.CommitFence(ctx, id, version, collidingToken)
}

func (c *collidingTokenCache[S]) ReleaseFence(ctx context.Context, id typeid.ID, version int64, _ aggregatestore.FenceToken) error {
	return c.AggregateCache.ReleaseFence(ctx, id, version, collidingToken)
}

// TestCachedStore_ConflictRefusalLeavesForeignReservationStanding pins the
// refusal contract: a reserve refused for a token/version conflict placed
// nothing, so the save's withdrawal must not run — releasing by token there
// would settle the conflicting pre-existing reservation, another save's
// floor, and re-admit publications beneath it.
func TestCachedStore_ConflictRefusalLeavesForeignReservationStanding(t *testing.T) {
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

	mem := aggregatestore.NewMemoryAggregateCache[account]()
	wrapper := &collidingTokenCache[account]{AggregateCache: mem}

	cached, err := aggregatestore.NewCachedStore[account](base, wrapper)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	// Seed durable version 1 without touching the cache.
	id := uuid.Must(uuid.NewV4())
	seed := base.New(id)
	seed.Append(fundsDeposited{Amount: 10})
	if err := base.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	aggregateID := typeid.New("account", id)

	// An outstanding reservation at version 10 — another in-flight save's
	// floor — held under the very token the wrapper collides with.
	if err := mem.ReserveFence(t.Context(), aggregateID, 10, collidingToken); err != nil {
		t.Fatalf("placing the pre-existing reservation: %v", err)
	}

	aggregate, err := base.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	aggregate.Append(fundsDeposited{Amount: 5})

	saveErr := cached.Save(t.Context(), aggregate, nil)
	if !errors.Is(saveErr, aggregatestore.ErrNoEventsAppended) || !errors.Is(saveErr, aggregatestore.ErrFenceReservationRefused) {
		t.Fatalf("want the save refused on the reservation conflict, got %v", saveErr)
	}

	// The version-10 floor still stands: a publication below it stays
	// refused.
	if err := mem.PutAggregate(t.Context(), aggregateID, aggregatestore.CachedAggregate[account]{
		State:   account{Balance: 99},
		Version: 5,
	}); err != nil {
		t.Fatalf("publication below the reservation: %v", err)
	}

	entry, err := mem.GetAggregate(t.Context(), aggregateID)
	if err != nil {
		t.Fatalf("reading the backing cache entry: %v", err)
	}
	if entry != nil {
		t.Errorf("want the version-5 publication refused by the standing version-10 reservation, got %+v (the conflict withdrawal released a foreign reservation)", entry)
	}
}

// TestCachedStore_RefusedReservationWithdrawsNothing pins the first layer of
// the refusal contract: a reserve error wrapping ErrFenceReservationRefused
// guarantees nothing was placed, so the save is refused without any
// settlement call — there is nothing to withdraw, and a release aimed at the
// refusing backend could only be aimed at someone else's reservation. (The
// second layer, version-addressed settlement, independently protects the
// conflicting reservation even from a caller that withdraws anyway.)
func TestCachedStore_RefusedReservationWithdrawsNothing(t *testing.T) {
	t.Parallel()

	settlements := 0
	cache := &mockCache[mockEntity]{
		ReserveFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
			return fmt.Errorf("%w: token collides", aggregatestore.ErrFenceReservationRefused)
		},
		ReleaseFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
			settlements++
			return nil
		},
		CommitFenceFn: func(context.Context, typeid.ID, int64, aggregatestore.FenceToken) error {
			settlements++
			return nil
		},
	}

	store, err := aggregatestore.NewCachedStore[mockEntity](&mockAggregateStore[mockEntity]{}, cache)
	if err != nil {
		t.Fatalf("creating cached store: %v", err)
	}

	aggregate := newMockAggregate(uuid.Must(uuid.NewV4()), 1)
	aggregate.Append(mockEntityEventA{})

	saveErr := store.Save(t.Context(), aggregate, nil)
	if !errors.Is(saveErr, aggregatestore.ErrNoEventsAppended) || !errors.Is(saveErr, aggregatestore.ErrFenceReservationRefused) {
		t.Fatalf("want the save refused with nothing appended on the refusal, got %v", saveErr)
	}

	if settlements != 0 {
		t.Errorf("want no settlement after a refused reservation, got %d calls", settlements)
	}
}
