package aggregatestore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
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

func TestHookableStore_New(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name               string
		haveInner          aggregatestore.Store[*mockEntity]
		havePrecreateHooks []aggregatestore.PrecreateHook
		haveHooks          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]
		wantAggregate      *aggregatestore.Aggregate[*mockEntity]
		wantErr            error
	}{
		{
			name: "creates a new aggregate using the inner store",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 42)
					return agg, nil
				},
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "creates a new aggregate using the inner store and calls a single pre-create hook",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 42)
					return agg, nil
				},
			},
			havePrecreateHooks: []aggregatestore.PrecreateHook{
				func(_ uuid.UUID) error {
					return errors.New("mock error")
				},
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("pre-create hook: mock error"),
		},
		{
			name: "creates a new aggregate using the inner store and calls multiple pre-create hooks",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 42)
					return agg, nil
				},
			},
			havePrecreateHooks: []aggregatestore.PrecreateHook{
				func(_ uuid.UUID) error {
					return nil
				},
				func(_ uuid.UUID) error {
					return nil
				},
				func(_ uuid.UUID) error {
					return errors.New("mock error")
				},
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("pre-create hook: mock error"),
		},
		{
			name: "creates a new aggregate using the inner store and calls a single after-create hook",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 42)
					return agg, nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.AfterNew: {
					func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("post-create hook: mock error"),
		},
		{
			name: "creates a new aggregate using the inner store and calls multiple after-create hooks",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 42)
					return agg, nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{
				aggregatestore.AfterNew: {
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
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: errors.New("post-create hook: mock error"),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(_ uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					return nil, errors.New("mock error")
				},
			},
			wantErr: errors.New("creating aggregate using inner store: mock error"),
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

			store.BeforeNew(tt.havePrecreateHooks...)
			store.AfterNew(tt.haveHooks[aggregatestore.AfterNew]...)

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

// func TestHookableStore_Load(t *testing.T) {
// 	t.Parallel()

// 	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

// 	for _, tt := range []struct {
// 		name               string
// 		haveInner          aggregatestore.Store[*mockEntity]
// 		havePrecreateHooks []aggregatestore.PrecreateHook
// 		havePreloadHooks   []aggregatestore.PreloadHook
// 		haveHooks          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]
// 		haveAggregateID    typeid.UUID
// 		haveOpts           aggregatestore.LoadOptions
// 		wantAggregate      *aggregatestore.Aggregate[*mockEntity]
// 		wantErr            error
// 	}{
// 		{
// 			name: "creates a new aggregate and hydrates it using default options",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregateID:    aggregateID,
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 12)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "passes the correct ToVersion hydrate option",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], opts aggregatestore.HydrateOptions) error {
// 					if opts.ToVersion != 42 {
// 						return fmt.Errorf("want hydrate opts ToVersion 42, got %d", opts.ToVersion)
// 					}

// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregateID:    aggregateID,
// 			haveOpts: aggregatestore.LoadOptions{
// 				ToVersion: 42,
// 			},
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "falls back to loading using the inner store when creating the aggregate fails",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(_ uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					return nil, errors.New("mock error")
// 				},
// 				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
// 					return agg, nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregateID:    aggregateID,
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "falls back to loading using the inner store when hydrating the aggregate fails",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
// 					return agg, nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregateID:    aggregateID,
// 			haveOpts: aggregatestore.LoadOptions{
// 				ToVersion: 42,
// 			},
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 		},
// 	} {
// 		t.Run(tt.name, func(t *testing.T) {
// 			t.Parallel()

// 			store, err := aggregatestore.NewHookableStore(
// 				tt.haveInner,
// 			)
// 			if err != nil {
// 				t.Fatalf("unexpected error creating store: %v", err)
// 			} else if store == nil {
// 				t.Fatal("unexpected nil store")
// 			}

// 			gotAggregate, gotErr := store.Load(context.Background(), tt.haveAggregateID, tt.haveOpts)

// 			if tt.wantErr != nil {
// 				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
// 					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
// 				}
// 				return
// 			}

// 			if gotErr != nil {
// 				t.Errorf("unexpected error: %v", gotErr)
// 			} else if gotAggregate == nil {
// 				t.Errorf("unexpected nil aggregate")
// 			}

// 			// aggregate has the correct ID
// 			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", aggregateID.UUID()).String() {
// 				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", aggregateID.UUID()), gotAggregate.ID())
// 			}
// 			// aggregate has the correct version
// 			if gotAggregate.Version() != tt.wantAggregate.Version() {
// 				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
// 			}
// 		})
// 	}
// }

// func TestHookableStore_Hydrate(t *testing.T) {
// 	t.Parallel()

// 	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

// 	for _, tt := range []struct {
// 		name               string
// 		haveInner          aggregatestore.Store[*mockEntity]
// 		havePrecreateHooks []aggregatestore.PrecreateHook
// 		havePreloadHooks   []aggregatestore.PreloadHook
// 		haveHooks          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]
// 		haveAggregate      *aggregatestore.Aggregate[*mockEntity]
// 		haveOpts           aggregatestore.HydrateOptions
// 		wantAggregate      *aggregatestore.Aggregate[*mockEntity]
// 		wantErr            error
// 	}{
// 		{
// 			name: "hydrates an aggregate to a snapshot version",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
// 				return agg
// 			}(),
// 			haveOpts: aggregatestore.HydrateOptions{
// 				ToVersion: 42,
// 			},
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "hydrates an aggregate to a snapshot version then further hydrates it using the inner store",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
// 				return agg
// 			}(),
// 		},
// 		// {
// 		// 	name: "passes the correct MaxVersion read snapshot option",
// 		// },
// 		{
// 			name: "falls back to hydrating using the inner store when already at the target version",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			haveOpts: aggregatestore.HydrateOptions{
// 				ToVersion: 42,
// 			},
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "falls back to hydrating using the inner store when target version is less than current version",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			haveOpts: aggregatestore.HydrateOptions{
// 				ToVersion: 37,
// 			},
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "falls back to hydrating using the inner store when reading a snapshot fails",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "falls back to hydrating using the inner store when no snapshot is available",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "falls back to hydrating using the inner store the snapshot cannot be unmarshaled",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
// 					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "returns an error when the aggregate is nil",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate:      nil,
// 			wantErr:            aggregatestore.HydrateAggregateError{Err: aggregatestore.ErrNilAggregate},
// 		},
// 		{
// 			name: "returns an error when the target version is invalid",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			haveOpts: aggregatestore.HydrateOptions{
// 				ToVersion: -1,
// 			},
// 			wantErr: aggregatestore.HydrateAggregateError{Err: errors.New("invalid target version")},
// 		},
// 		{
// 			name: "returns an error when the snapshot stoe reader is nil",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
// 					agg := &aggregatestore.Aggregate[*mockEntity]{}
// 					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
// 					return agg, nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantErr: aggregatestore.HydrateAggregateError{Err: errors.New("snapshot store has no snapshot reader")},
// 		},
// 	} {
// 		t.Run(tt.name, func(t *testing.T) {
// 			t.Parallel()

// 			store, err := aggregatestore.NewHookableStore(
// 				tt.haveInner,
// 			)
// 			if err != nil {
// 				t.Fatalf("unexpected error creating store: %v", err)
// 			} else if store == nil {
// 				t.Fatal("unexpected nil store")
// 			}

// 			gotAggregate := tt.haveAggregate
// 			gotErr := store.Hydrate(context.Background(), gotAggregate, tt.haveOpts)

// 			if tt.wantErr != nil {
// 				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
// 					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
// 				}
// 				return
// 			}

// 			if gotErr != nil {
// 				t.Errorf("unexpected error: %v", gotErr)
// 			} else if gotAggregate == nil {
// 				t.Errorf("unexpected nil aggregate")
// 			}

// 			// aggregate has the correct ID
// 			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", aggregateID.UUID()).String() {
// 				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", aggregateID.UUID()), gotAggregate.ID())
// 			}
// 			// aggregate has the correct version
// 			if gotAggregate.Version() != tt.wantAggregate.Version() {
// 				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
// 			}
// 		})
// 	}
// }

// func TestHookableStore_Save(t *testing.T) {
// 	t.Parallel()

// 	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

// 	for _, tt := range []struct {
// 		name               string
// 		haveInner          aggregatestore.Store[*mockEntity]
// 		havePrecreateHooks []aggregatestore.PrecreateHook
// 		havePreloadHooks   []aggregatestore.PreloadHook
// 		haveHooks          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]
// 		haveAggregate      *aggregatestore.Aggregate[*mockEntity]
// 		haveOpts           aggregatestore.SaveOptions
// 		wantAggregate      *aggregatestore.Aggregate[*mockEntity]
// 		wantErr            error
// 	}{
// 		{
// 			name: "saves an aggregate using the inner store and creates no snapshot if the policy does not require it",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
// 					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
// 						EntityEvent: &mockEntityEventA{},
// 					})
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{
// 					ID:         aggregateID,
// 					eventTypes: []estoria.EntityEvent{mockEntityEventA{}},
// 				}, 42)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 43)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "saves an aggregate using the inner store and creates no snapshot if the snapshot fails to marshal",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
// 					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
// 						EntityEvent: &mockEntityEventA{},
// 					})
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 43)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "saves an aggregate using the inner store and creates no snapshot if the snappshot writer fails to write",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
// 					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
// 						EntityEvent: &mockEntityEventA{},
// 					})
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 43)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name: "saves an aggregate using the inner store and creates a snapshot if the policy requires it",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
// 					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
// 						EntityEvent: &mockEntityEventA{},
// 					})
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 43)
// 				return agg
// 			}(),
// 		},
// 		{
// 			name:               "returns an error when the aggregate is nil",
// 			haveInner:          &mockAggregateStore[*mockEntity]{},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate:      nil,
// 			wantErr:            aggregatestore.SaveAggregateError{Err: aggregatestore.ErrNilAggregate},
// 		},
// 		{
// 			name: "returns an error when the inner store returns an error",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				SaveFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
// 					return errors.New("mock error")
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
// 				return agg
// 			}(),
// 			wantErr: aggregatestore.SaveAggregateError{Err: errors.New("saving aggregate using inner store: mock error")},
// 		},
// 		{
// 			name: "returns an error when encountering an unexpected error applying an event",
// 			haveInner: &mockAggregateStore[*mockEntity]{
// 				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
// 					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
// 						EntityEvent: &mockEntityEventA{},
// 					})
// 					return nil
// 				},
// 			},
// 			havePrecreateHooks: []aggregatestore.PrecreateHook{},
// 			havePreloadHooks:   []aggregatestore.PreloadHook{},
// 			haveHooks:          map[aggregatestore.HookStage][]aggregatestore.Hook[*mockEntity]{},
// 			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
// 				agg := &aggregatestore.Aggregate[*mockEntity]{}
// 				agg.State().SetEntityAtVersion(&mockEntity{
// 					ID:            aggregateID,
// 					applyEventErr: errors.New("mock error"),
// 				}, 42)
// 				return agg
// 			}(),
// 			wantErr: aggregatestore.SaveAggregateError{Err: errors.New("applying next aggregate event: applying event: mock error")},
// 		},
// 	} {
// 		t.Run(tt.name, func(t *testing.T) {
// 			t.Parallel()

// 			store, err := aggregatestore.NewHookableStore(tt.haveInner)
// 			if err != nil {
// 				t.Fatalf("unexpected error creating store: %v", err)
// 			} else if store == nil {
// 				t.Fatal("unexpected nil store")
// 			}

// 			gotAggregate := tt.haveAggregate
// 			gotErr := store.Save(context.Background(), gotAggregate, tt.haveOpts)

// 			if tt.wantErr != nil {
// 				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
// 					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
// 				}
// 				return
// 			}

// 			if gotErr != nil {
// 				t.Errorf("unexpected error: %v", gotErr)
// 			} else if gotAggregate == nil {
// 				t.Errorf("unexpected nil aggregate")
// 			}

// 			// aggregate has the correct ID
// 			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", aggregateID.UUID()).String() {
// 				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", aggregateID.UUID()), gotAggregate.ID())
// 			}
// 			// aggregate has the correct version
// 			if gotAggregate.Version() != tt.wantAggregate.Version() {
// 				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
// 			}
// 		})
// 	}
// }
