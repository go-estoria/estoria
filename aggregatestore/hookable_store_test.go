package aggregatestore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
)

func TestNewHookableStore(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		haveInner aggregatestore.Store[*mockEntity]
		wantErr   error
	}{
		{
			name:      "creates a new hookable store",
			haveInner: &mockAggregateStore[*mockEntity]{},
		},
		{
			name:      "returns an error when the inner store is nil",
			haveInner: nil,
			wantErr:   errors.New("inner store is required"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStore, gotErr := aggregatestore.NewHookableStore(tt.haveInner)
			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if gotStore == nil {
				t.Error("unexpected nil store")
			}
		})
	}
}

func TestHookableStore_Load(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name             string
		haveInner        aggregatestore.Store[*mockEntity]
		havePreloadHooks []aggregatestore.PreloadHook
		haveHooks        map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]
		haveAggregateID  typeid.UUID
		haveOpts         aggregatestore.LoadOptions
		wantAggregate    *aggregatestore.Aggregate[*mockEntity]
		wantErr          error
	}{
		{
			name: "loads an aggergate using the inner store when no hooks are provided",
			haveInner: &mockAggregateStore[*mockEntity]{
				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
					return agg, nil
				},
			},
			havePreloadHooks: []aggregatestore.PreloadHook{},
			haveHooks:        map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
			haveAggregateID:  aggregateID,
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "loads an aggergate using the inner store and runs a single pre-load hook",
			haveInner: &mockAggregateStore[*mockEntity]{
				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
					return agg, nil
				},
			},
			havePreloadHooks: []aggregatestore.PreloadHook{
				func(_ context.Context, _ typeid.UUID) error {
					return errors.New("mock error")
				},
			},
			haveHooks:       map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("pre-load hook: mock error"),
		},
		{
			name: "loads an aggergate using the inner store and runs multiple pre-load hooks",
			haveInner: &mockAggregateStore[*mockEntity]{
				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
					return agg, nil
				},
			},
			havePreloadHooks: []aggregatestore.PreloadHook{
				func(_ context.Context, _ typeid.UUID) error {
					return nil
				},
				func(_ context.Context, _ typeid.UUID) error {
					return nil
				},
				func(_ context.Context, _ typeid.UUID) error {
					return errors.New("mock error")
				},
			},
			haveHooks:       map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("pre-load hook: mock error"),
		},
		{
			name: "loads an aggergate using the inner store and runs a single post-load hook",
			haveInner: &mockAggregateStore[*mockEntity]{
				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
					return agg, nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.AfterLoad: {
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("post-load hook: mock error"),
		},
		{
			name: "loads an aggergate using the inner store and runs multiple post-load hooks",
			haveInner: &mockAggregateStore[*mockEntity]{
				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
					return agg, nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.AfterLoad: {
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("post-load hook: mock error"),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[*mockEntity]{
				LoadFn: func(_ context.Context, _ typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					return nil, errors.New("mock error")
				},
			},
			wantErr: errors.New("loading aggregate using inner store: mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewHookableStore(
				tt.haveInner,
			)
			if err != nil {
				t.Fatalf("unexpected error creating store: %v", err)
			} else if store == nil {
				t.Fatal("unexpected nil store")
			}

			store.BeforeLoad(tt.havePreloadHooks...)
			store.AfterLoad(tt.haveHooks[aggregatestore.AfterLoad]...)

			gotAggregate, gotErr := store.Load(context.Background(), tt.haveAggregateID, tt.haveOpts)

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

func TestHookableStore_Hydrate(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name          string
		haveInner     aggregatestore.Store[*mockEntity]
		haveAggregate *aggregatestore.Aggregate[*mockEntity]
		haveOpts      aggregatestore.HydrateOptions
		wantAggregate *aggregatestore.Aggregate[*mockEntity]
		wantErr       error
	}{
		{
			name: "hydrates an aggregate using the inner store",
			haveInner: &mockAggregateStore[*mockEntity]{
				HydrateFn: func(_ context.Context, agg *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
					return nil
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
				return agg
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewHookableStore(
				tt.haveInner,
			)
			if err != nil {
				t.Fatalf("unexpected error creating store: %v", err)
			} else if store == nil {
				t.Fatal("unexpected nil store")
			}

			gotAggregate := tt.haveAggregate
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

func TestHookableStore_Save(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name          string
		haveInner     aggregatestore.Store[*mockEntity]
		haveHooks     map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]
		haveAggregate *aggregatestore.Aggregate[*mockEntity]
		haveOpts      aggregatestore.SaveOptions
		wantAggregate *aggregatestore.Aggregate[*mockEntity]
		wantErr       error
	}{
		{
			name: "saves an aggregate using the inner store when no hooks are provided",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 43)
				return agg
			}(),
		},
		{
			name: "saves an aggregate using the inner store when a single pre-save hook is provided",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.BeforeSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("pre-save hook: mock error"),
		},
		{
			name: "saves an aggregate using the inner store when multiple pre-save hooks are provided",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.BeforeSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("pre-save hook: mock error"),
		},
		{
			name: "saves an aggregate using the inner store when a single post-save hook is provided",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.AfterSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("post-save hook: mock error"),
		},
		{
			name: "saves an aggregate using the inner store when multiple post-save hooks are provided",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.AfterSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("post-save hook: mock error"),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					return errors.New("mock error")
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("saving aggregate using inner store: mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewHookableStore(tt.haveInner)
			if err != nil {
				t.Fatalf("unexpected error creating store: %v", err)
			} else if store == nil {
				t.Fatal("unexpected nil store")
			}

			store.BeforeSave(tt.haveHooks[aggregatestore.BeforeSave]...)
			store.AfterSave(tt.haveHooks[aggregatestore.AfterSave]...)

			gotAggregate := tt.haveAggregate
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
		})
	}
}
