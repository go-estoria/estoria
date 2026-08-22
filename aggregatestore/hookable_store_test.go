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
		haveInner aggregatestore.Store[mockEntity]
		wantErr   error
	}{
		{
			name:      "creates a new hookable store",
			haveInner: &mockAggregateStore[mockEntity]{},
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

	aggregateID := uuid.Must(uuid.NewV4())

	for _, tt := range []struct {
		name             string
		haveInner        aggregatestore.Store[mockEntity]
		havePreloadHooks []aggregatestore.PreloadHook
		haveHooks        map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]
		haveAggregateID  uuid.UUID
		haveOpts         *aggregatestore.LoadOptions
		wantAggregate    *aggregatestore.Aggregate[mockEntity]
		wantErr          error
	}{
		{
			name: "loads an aggergate using the inner store when no hooks are provided",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				LoadFn: func(_ context.Context, id uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
					return newMockAggregate(id, 42), nil
				},
			},
			havePreloadHooks: []aggregatestore.PreloadHook{},
			haveHooks:        map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{},
			haveAggregateID:  aggregateID,
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "loads an aggergate using the inner store and runs a single pre-load hook",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				LoadFn: func(_ context.Context, id uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
					return newMockAggregate(id, 42), nil
				},
			},
			havePreloadHooks: []aggregatestore.PreloadHook{
				func(_ context.Context, _ uuid.UUID) error {
					return errors.New("mock error")
				},
			},
			haveHooks:       map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("pre-load hook: mock error"),
		},
		{
			name: "loads an aggergate using the inner store and runs multiple pre-load hooks",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				LoadFn: func(_ context.Context, id uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
					return newMockAggregate(id, 42), nil
				},
			},
			havePreloadHooks: []aggregatestore.PreloadHook{
				func(_ context.Context, _ uuid.UUID) error {
					return nil
				},
				func(_ context.Context, _ uuid.UUID) error {
					return nil
				},
				func(_ context.Context, _ uuid.UUID) error {
					return errors.New("mock error")
				},
			},
			haveHooks:       map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("pre-load hook: mock error"),
		},
		{
			name: "loads an aggergate using the inner store and runs a single post-load hook",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				LoadFn: func(_ context.Context, id uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
					return newMockAggregate(id, 42), nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{
				aggregatestore.AfterLoad: {
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("post-load hook: mock error"),
		},
		{
			name: "loads an aggergate using the inner store and runs multiple post-load hooks",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				LoadFn: func(_ context.Context, id uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
					return newMockAggregate(id, 42), nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{
				aggregatestore.AfterLoad: {
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregateID: aggregateID,
			wantErr:         errors.New("post-load hook: mock error"),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				LoadFn: func(_ context.Context, _ uuid.UUID, _ *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[mockEntity], error) {
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

			gotAggregate, gotErr := store.Load(t.Context(), tt.haveAggregateID, tt.haveOpts)

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

func TestHookableStore_Hydrate(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	for _, tt := range []struct {
		name          string
		haveInner     aggregatestore.Store[mockEntity]
		haveAggregate *aggregatestore.Aggregate[mockEntity]
		haveOpts      *aggregatestore.HydrateOptions
		wantAggregate *aggregatestore.Aggregate[mockEntity]
		wantErr       error
	}{
		{
			name: "hydrates an aggregate using the inner store",
			haveInner: &mockAggregateStore[mockEntity]{
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), 42)
					return nil
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 0)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
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

func TestHookableStore_Save(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	for _, tt := range []struct {
		name          string
		haveInner     aggregatestore.Store[mockEntity]
		haveHooks     map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]
		haveAggregate *aggregatestore.Aggregate[mockEntity]
		haveOpts      *aggregatestore.SaveOptions
		wantAggregate *aggregatestore.Aggregate[mockEntity]
		wantErr       error
	}{
		{
			name: "saves an aggregate using the inner store when no hooks are provided",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 43)
			}(),
		},
		{
			name: "saves an aggregate using the inner store when a single pre-save hook is provided",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{
				aggregatestore.BeforeSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
			wantErr: errors.New("pre-save hook: mock error"),
		},
		{
			name: "saves an aggregate using the inner store when multiple pre-save hooks are provided",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{
				aggregatestore.BeforeSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
			wantErr: errors.New("pre-save hook: mock error"),
		},
		{
			name: "saves an aggregate using the inner store when a single post-save hook is provided",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{
				aggregatestore.AfterSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
			wantErr: errors.New("post-save hook: mock error"),
		},
		{
			name: "saves an aggregate using the inner store when multiple post-save hooks are provided",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlySetStateAtVersion(newMockEntity(aggregateID), aggregate.Version()+1)
					return nil
				},
			},
			haveHooks: map[aggregatestore.HookStage][]aggregatestore.Hook[mockEntity]{
				aggregatestore.AfterSave: {
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return nil
					},
					func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity]) error {
						return errors.New("mock error")
					},
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
			wantErr: errors.New("post-save hook: mock error"),
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					return errors.New("mock error")
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
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

// TestHookableStore_Save_MarksHookErrorOutcomes pins the save-outcome
// markers hook failures carry: a pre-save hook error refused a save that
// appended nothing, a post-save hook error following a save that appended
// queued events reports their facts, and a post-save hook error following a
// no-op save — nothing was queued, so nothing was appended — must not
// report facts that do not exist.
func TestHookableStore_Save_MarksHookErrorOutcomes(t *testing.T) {
	t.Parallel()

	newStore := func(t *testing.T) *aggregatestore.HookableStore[mockEntity] {
		t.Helper()

		store, err := aggregatestore.NewHookableStore[mockEntity](&mockAggregateStore[mockEntity]{
			SaveFn: func(context.Context, *aggregatestore.Aggregate[mockEntity], *aggregatestore.SaveOptions) error {
				return nil
			},
		})
		if err != nil {
			t.Fatalf("creating hookable store: %v", err)
		}

		return store
	}

	t.Run("a pre-save hook error reports nothing appended", func(t *testing.T) {
		t.Parallel()

		store := newStore(t)
		store.BeforeSave(func(context.Context, *aggregatestore.Aggregate[mockEntity]) error {
			return errors.New("refused")
		})

		err := store.Save(t.Context(), newMockAggregate(uuid.Must(uuid.NewV4()), 1), nil)
		if !errors.Is(err, aggregatestore.ErrNoEventsAppended) {
			t.Errorf("want the pre-save hook error carrying ErrNoEventsAppended, got %v", err)
		}
		if errors.Is(err, aggregatestore.ErrEventsAppended) {
			t.Errorf("want no ErrEventsAppended before the inner save ran, got %v", err)
		}
	})

	t.Run("a post-save hook error reports queued events appended", func(t *testing.T) {
		t.Parallel()

		store := newStore(t)
		store.AfterSave(func(context.Context, *aggregatestore.Aggregate[mockEntity]) error {
			return errors.New("side effect failed")
		})

		aggregate := newMockAggregate(uuid.Must(uuid.NewV4()), 1)
		aggregate.Append(mockEntityEventA{})

		err := store.Save(t.Context(), aggregate, nil)
		if got := aggregatestore.SaveOutcome(err); got != aggregatestore.AppendOutcomeAppended {
			t.Errorf("want the post-save hook error resolving to ErrEventsAppended, got %v (error: %v)", got, err)
		}
	})

	t.Run("a post-save hook error after a no-op save reports nothing appended", func(t *testing.T) {
		t.Parallel()

		store := newStore(t)
		store.AfterSave(func(context.Context, *aggregatestore.Aggregate[mockEntity]) error {
			return errors.New("side effect failed")
		})

		// Version 1 with nothing queued: the save appends nothing.
		err := store.Save(t.Context(), newMockAggregate(uuid.Must(uuid.NewV4()), 1), nil)
		if got := aggregatestore.SaveOutcome(err); got != aggregatestore.AppendOutcomeNothingAppended {
			t.Errorf("want the no-op save's post-hook error resolving to ErrNoEventsAppended, got %v (error: %v)", got, err)
		}
	})

	t.Run("a post-save hook error after hooks queued the only events reports them appended", func(t *testing.T) {
		t.Parallel()

		store := newStore(t)
		store.BeforeSave(func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity]) error {
			aggregate.Append(mockEntityEventA{})
			return nil
		})
		store.AfterSave(func(context.Context, *aggregatestore.Aggregate[mockEntity]) error {
			return errors.New("side effect failed")
		})

		// Nothing queued by the caller: what the inner store appends is
		// exactly what the pre-save hooks queued.
		err := store.Save(t.Context(), newMockAggregate(uuid.Must(uuid.NewV4()), 1), nil)
		if got := aggregatestore.SaveOutcome(err); got != aggregatestore.AppendOutcomeAppended {
			t.Errorf("want the hook-queued events' facts reported appended, got %v (error: %v)", got, err)
		}
	})
}
