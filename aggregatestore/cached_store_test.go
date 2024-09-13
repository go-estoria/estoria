package aggregatestore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatecache"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
)

type mockCache[E estoria.Entity] struct {
	getAggregateErr error
	putAggregateErr error
}

func (c *mockCache[E]) GetAggregate(_ context.Context, _ typeid.UUID) (*aggregatestore.Aggregate[E], error) {
	return nil, c.getAggregateErr
}

func (c *mockCache[E]) PutAggregate(_ context.Context, _ *aggregatestore.Aggregate[E]) error {
	return c.putAggregateErr
}

func TestNewCachedStore(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		haveInner func() aggregatestore.Store[*mockEntity]
		haveCache func() aggregatestore.AggregateCache[*mockEntity]
		wantErr   error
	}{
		{
			name: "creates a new cached store",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStore := aggregatestore.NewCachedStore(tt.haveInner(), tt.haveCache())

			if gotStore == nil {
				t.Error("unexpected nil store")
			}
		})
	}
}

func TestCachedStore_New(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name          string
		haveInner     func() aggregatestore.Store[*mockEntity]
		haveCache     func() aggregatestore.AggregateCache[*mockEntity]
		wantAggregate *aggregatestore.Aggregate[*mockEntity]
		wantErr       error
	}{
		{
			name: "creates a new aggregate using the inner store",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					newAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg
					}(),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					newErr: errors.New("mock error"),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			wantErr: errors.New("mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := aggregatestore.NewCachedStore(tt.haveInner(), tt.haveCache())
			if store == nil {
				t.Fatal("unexpected nil store")
			}

			gotAggregate, gotErr := store.New(aggregateID.UUID())

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
			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", aggregateID.UUID()).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", aggregateID.UUID()), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantAggregate.Version() {
				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
			}
		})
	}
}

func TestCachedStore_Load(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name          string
		haveInner     func() aggregatestore.Store[*mockEntity]
		haveCache     func() aggregatestore.AggregateCache[*mockEntity]
		haveOpts      aggregatestore.LoadOptions
		wantAggregate *aggregatestore.Aggregate[*mockEntity]
		wantErr       error
	}{
		{
			name: "returns an aggregate from the cache when available",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					loadErr: errors.New("unexpected attempt to load aggregate from inner store"),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				cache := aggregatecache.NewInMemoryCache[*mockEntity]()
				cache.PutAggregate(context.Background(), func() *aggregatestore.Aggregate[*mockEntity] {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
					return agg
				}())
				return cache
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "returns an aggregate from the inner store when the aggregate is not found in the cache",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					loadedAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg
					}(),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "returns an aggregate from the inner store when the cache returns an error",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					loadedAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg
					}(),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					getAggregateErr: errors.New("mock error"),
				}
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					loadErr: errors.New("mock error"),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			wantErr: errors.New("loading from inner aggregate store: mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := aggregatestore.NewCachedStore(tt.haveInner(), tt.haveCache())
			if store == nil {
				t.Fatal("unexpected nil store")
			}

			gotAggregate, gotErr := store.Load(context.Background(), aggregateID, tt.haveOpts)

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
			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", aggregateID.UUID()).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", aggregateID.UUID()), gotAggregate.ID())
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

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name          string
		haveInner     func() aggregatestore.Store[*mockEntity]
		haveCache     func() aggregatestore.AggregateCache[*mockEntity]
		haveOpts      aggregatestore.HydrateOptions
		haveAggregate func() *aggregatestore.Aggregate[*mockEntity]
		wantAggregate *aggregatestore.Aggregate[*mockEntity]
		wantErr       error
	}{
		{
			name: "hydrates an aggregate using the inner store",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					hydratedAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg
					}(),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
				return agg
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					hydrateErr: errors.New("mock error"),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				return nil
			},
			wantErr: errors.New("mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := aggregatestore.NewCachedStore(tt.haveInner(), tt.haveCache())
			if store == nil {
				t.Fatal("unexpected nil store")
			}

			gotAggregate := tt.haveAggregate()
			gotErr := store.Hydrate(context.Background(), gotAggregate, tt.haveOpts)

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
			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", aggregateID.UUID()).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", aggregateID.UUID()), gotAggregate.ID())
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

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name                string
		haveInner           func() aggregatestore.Store[*mockEntity]
		haveCache           func() aggregatestore.AggregateCache[*mockEntity]
		haveOpts            aggregatestore.SaveOptions
		haveAggregate       func() *aggregatestore.Aggregate[*mockEntity]
		wantAggregate       *aggregatestore.Aggregate[*mockEntity]
		wantCachedAggregate *aggregatestore.Aggregate[*mockEntity]
		wantErr             error
	}{
		{
			name: "saves an aggregate using the inner store and adds the aggregate to the cache",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					savedAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg
					}(),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
				return agg
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantCachedAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					saveErr: errors.New("mock error"),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return aggregatecache.NewInMemoryCache[*mockEntity]()
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			},
			wantErr: errors.New("saving to inner aggregate store: mock error"),
		},
		{
			name: "does not return an error when failing to add the aggregate to the cache",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockStore[*mockEntity]{
					savedAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg
					}(),
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					putAggregateErr: errors.New("mock error"),
				}
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
				return agg
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			haveCache := tt.haveCache()

			store := aggregatestore.NewCachedStore(tt.haveInner(), haveCache)
			if store == nil {
				t.Fatal("unexpected nil store")
			}

			gotAggregate := tt.haveAggregate()
			gotErr := store.Save(context.Background(), gotAggregate, tt.haveOpts)

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
			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", aggregateID.UUID()).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", aggregateID.UUID()), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantAggregate.Version() {
				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
			}

			if tt.wantCachedAggregate == nil {
				return
			}

			gotCachedAggregate, gotErr := haveCache.GetAggregate(context.Background(), aggregateID)
			if gotErr != nil {
				t.Fatalf("unexpected error getting cached aggregate: %v", gotErr)
			}

			if gotCachedAggregate == nil {
				t.Errorf("unexpected nil cached aggregate")
				return
			}

			// cached aggregate has the correct ID
			if gotCachedAggregate.ID().String() != tt.wantCachedAggregate.ID().String() {
				t.Errorf("want cached aggregate ID %s, got %s", tt.wantCachedAggregate.ID(), gotCachedAggregate.ID())
			}
			// cached aggregate has the correct version
			if gotCachedAggregate.Version() != tt.wantCachedAggregate.Version() {
				t.Errorf("want cached aggregate version %d, got %d", tt.wantCachedAggregate.Version(), gotCachedAggregate.Version())
			}
		})
	}
}
