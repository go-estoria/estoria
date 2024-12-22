package aggregatestore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
)

// mockCache is a mock implementation of aggregatestore.AggregateCache.
type mockCache[E estoria.Entity] struct {
	GetAggregateFunc func(context.Context, typeid.UUID) (*aggregatestore.Aggregate[E], error)
	PutAggregateFunc func(context.Context, *aggregatestore.Aggregate[E]) error
}

func (c *mockCache[E]) GetAggregate(ctx context.Context, id typeid.UUID) (*aggregatestore.Aggregate[E], error) {
	if c.GetAggregateFunc != nil {
		return c.GetAggregateFunc(ctx, typeid.UUID{})
	}

	return nil, fmt.Errorf("unexpected call: GetAggregate(id=%s)", id)
}

func (c *mockCache[E]) PutAggregate(ctx context.Context, _ *aggregatestore.Aggregate[E]) error {
	if c.PutAggregateFunc != nil {
		return c.PutAggregateFunc(ctx, nil)
	}

	return fmt.Errorf("unexpected call: PutAggregate(aggregate=%v)", nil)
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
				return &mockAggregateStore[*mockEntity]{}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{}
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
				return &mockAggregateStore[*mockEntity]{}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					GetAggregateFunc: func(context.Context, typeid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg, nil
					},
				}
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
				return &mockAggregateStore[*mockEntity]{
					LoadFn: func(context.Context, typeid.UUID, aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg, nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					GetAggregateFunc: func(context.Context, typeid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
						return nil, nil
					},
				}
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
				return &mockAggregateStore[*mockEntity]{
					LoadFn: func(context.Context, typeid.UUID, aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
						agg := &aggregatestore.Aggregate[*mockEntity]{}
						agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return agg, nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					GetAggregateFunc: func(context.Context, typeid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
						return nil, errors.New("mock error")
					},
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
				return &mockAggregateStore[*mockEntity]{
					LoadFn: func(context.Context, typeid.UUID, aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
						return nil, errors.New("mock error")
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					GetAggregateFunc: func(context.Context, typeid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
						return nil, nil
					},
				}
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
				return &mockAggregateStore[*mockEntity]{
					HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
						aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{}
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
				return &mockAggregateStore[*mockEntity]{
					HydrateFn: func(context.Context, *aggregatestore.Aggregate[*mockEntity], aggregatestore.HydrateOptions) error {
						return errors.New("mock error")
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{}
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
				return &mockAggregateStore[*mockEntity]{
					SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
						aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					PutAggregateFunc: func(context.Context, *aggregatestore.Aggregate[*mockEntity]) error {
						return nil
					},
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
			wantCachedAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: func() aggregatestore.Store[*mockEntity] {
				return &mockAggregateStore[*mockEntity]{
					SaveFn: func(context.Context, *aggregatestore.Aggregate[*mockEntity], aggregatestore.SaveOptions) error {
						return errors.New("mock error")
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{}
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
				return &mockAggregateStore[*mockEntity]{
					SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
						aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
						return nil
					},
				}
			},
			haveCache: func() aggregatestore.AggregateCache[*mockEntity] {
				return &mockCache[*mockEntity]{
					PutAggregateFunc: func(context.Context, *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
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
		})
	}
}
