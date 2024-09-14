package aggregatestore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type mockSnapshotStore struct {
	ReadSnapshotFn  func(context.Context, typeid.UUID, snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error)
	WriteSnapshotFn func(context.Context, *snapshotstore.AggregateSnapshot) error
}

func (m *mockSnapshotStore) ReadSnapshot(ctx context.Context, aggregateID typeid.UUID, opts snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
	if m.ReadSnapshotFn != nil {
		return m.ReadSnapshotFn(ctx, aggregateID, opts)
	}

	return nil, errors.New("unexpected call to ReadSnapshot")
}

func (m *mockSnapshotStore) WriteSnapshot(ctx context.Context, snapshot *snapshotstore.AggregateSnapshot) error {
	if m.WriteSnapshotFn != nil {
		return m.WriteSnapshotFn(ctx, snapshot)
	}

	return errors.New("unexpected call to WriteSnapshot")
}

type mockSnapshotPolicy struct {
	ShouldSnapshotFn func(typeid.UUID, int64, time.Time) bool
}

func (m *mockSnapshotPolicy) ShouldSnapshot(aggregateID typeid.UUID, version int64, timestamp time.Time) bool {
	if m.ShouldSnapshotFn != nil {
		return m.ShouldSnapshotFn(aggregateID, version, timestamp)
	}

	return false
}

type mockSnapshotMarshaler struct {
	MarshalFn   func(**mockEntity) ([]byte, error)
	UnmarshalFn func([]byte, **mockEntity) error
}

func (m *mockSnapshotMarshaler) Marshal(entity **mockEntity) ([]byte, error) {
	if m.MarshalFn != nil {
		return m.MarshalFn(entity)
	}

	return nil, errors.New("unexpected call to Marshal")
}

func (m *mockSnapshotMarshaler) Unmarshal(data []byte, entity **mockEntity) error {
	if m.UnmarshalFn != nil {
		return m.UnmarshalFn(data, entity)
	}

	return errors.New("unexpected call to Unmarshal")
}

func TestNewSnapshottingStore(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name               string
		haveInner          aggregatestore.Store[*mockEntity]
		haveSnapshotStore  aggregatestore.SnapshotStore
		haveSnapshotPolicy aggregatestore.SnapshotPolicy
		haveOpts           []aggregatestore.SnapshottingStoreOption[*mockEntity]
		wantErr            error
	}{
		{
			name:               "creates a new snapshotting store with default options",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
		},
		{
			name:               "creates a new snapshotting store with a custom snapshot marshaler",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[*mockEntity]{
				aggregatestore.WithSnapshotMarshaler(estoria.JSONMarshaler[*mockEntity]{}),
			},
		},
		{
			name:               "creates a new snapshotting store with a custom snapshot reader",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[*mockEntity]{
				aggregatestore.WithSnapshotReader[*mockEntity](&mockSnapshotStore{}),
			},
		},
		{
			name:               "creates a new snapshotting store with a custom snapshot writer",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[*mockEntity]{
				aggregatestore.WithSnapshotWriter[*mockEntity](&mockSnapshotStore{}),
			},
		},
		{
			name:               "returns an error when the inner store is nil",
			haveInner:          nil,
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			wantErr:            errors.New("inner store is required"),
		},
		{
			name:               "returns an error when the snapshot store is nil",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  nil,
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			wantErr:            errors.New("snapshot store is required"),
		},
		{
			name:               "returns an error when the snapshot policy is nil",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: nil,
			wantErr:            errors.New("snapshot policy is required"),
		},
		{
			name:               "returns an error when applying an option fails",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[*mockEntity]{
				func(*aggregatestore.SnapshottingStore[*mockEntity]) error {
					return errors.New("mock error")
				},
			},
			wantErr: errors.New("applying option: mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStore, gotErr := aggregatestore.NewSnapshottingStore(
				tt.haveInner,
				tt.haveSnapshotStore,
				tt.haveSnapshotPolicy,
				tt.haveOpts...,
			)
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

func TestSnapshottingStore_New(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name          string
		haveInner     aggregatestore.Store[*mockEntity]
		wantAggregate *aggregatestore.Aggregate[*mockEntity]
		wantErr       error
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
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(_ uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					return nil, errors.New("mock error")
				},
			},
			wantErr: errors.New("mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewSnapshottingStore(
				tt.haveInner,
				&mockSnapshotStore{},
				&mockSnapshotPolicy{},
			)
			if err != nil {
				t.Fatalf("unexpected error creating store: %v", err)
			} else if store == nil {
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

func TestSnapshottingStore_Load(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name                      string
		haveInner                 aggregatestore.Store[*mockEntity]
		haveSnapshotStore         aggregatestore.SnapshotStore
		haveSnapshottingStoreOpts []aggregatestore.SnapshottingStoreOption[*mockEntity]
		haveAggregateID           typeid.UUID
		haveOpts                  aggregatestore.LoadOptions
		wantAggregate             *aggregatestore.Aggregate[*mockEntity]
		wantErr                   error
	}{
		{
			name: "creates a new aggregate and hydrates it using default options",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 12,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregateID: aggregateID,
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 12)
				return agg
			}(),
		},
		{
			name: "passes the correct ToVersion hydrate option",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], opts aggregatestore.HydrateOptions) error {
					if opts.ToVersion != 42 {
						return fmt.Errorf("want hydrate opts ToVersion 42, got %d", opts.ToVersion)
					}

					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 42,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregateID: aggregateID,
			haveOpts: aggregatestore.LoadOptions{
				ToVersion: 42,
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "falls back to loading using the inner store when creating the aggregate fails",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(_ uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					return nil, errors.New("mock error")
				},
				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
					return agg, nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregateID:   aggregateID,
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "falls back to loading using the inner store when hydrating the aggregate fails",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				LoadFn: func(_ context.Context, id typeid.UUID, _ aggregatestore.LoadOptions) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: id}, 42)
					return agg, nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return nil, errors.New("mock error")
				},
			},
			haveAggregateID: aggregateID,
			haveOpts: aggregatestore.LoadOptions{
				ToVersion: 42,
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

			store, err := aggregatestore.NewSnapshottingStore(
				tt.haveInner,
				tt.haveSnapshotStore,
				&mockSnapshotPolicy{},
				tt.haveSnapshottingStoreOpts...,
			)
			if err != nil {
				t.Fatalf("unexpected error creating store: %v", err)
			} else if store == nil {
				t.Fatal("unexpected nil store")
			}

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

func TestSnapshottingStore_Hydrate(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name                      string
		haveInner                 aggregatestore.Store[*mockEntity]
		haveSnapshotStore         aggregatestore.SnapshotStore
		haveSnapshottingStoreOpts []aggregatestore.SnapshottingStoreOption[*mockEntity]
		haveAggregate             *aggregatestore.Aggregate[*mockEntity]
		haveOpts                  aggregatestore.HydrateOptions
		wantAggregate             *aggregatestore.Aggregate[*mockEntity]
		wantErr                   error
	}{
		{
			name: "hydrates an aggregate to a snapshot version",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 42,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
				return agg
			}(),
			haveOpts: aggregatestore.HydrateOptions{
				ToVersion: 42,
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "hydrates an aggregate to a snapshot version then further hydrates it using the inner store",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 42,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 0)
				return agg
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
				return agg
			}(),
		},
		// {
		// 	name: "passes the correct MaxVersion read snapshot option",
		// },
		{
			name: "falls back to hydrating using the inner store when already at the target version",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			haveOpts: aggregatestore.HydrateOptions{
				ToVersion: 42,
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when target version is less than current version",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			haveOpts: aggregatestore.HydrateOptions{
				ToVersion: 37,
			},
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when reading a snapshot fails",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return nil, errors.New("mock error")
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
				return agg
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when no snapshot is available",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return nil, snapshotstore.ErrSnapshotNotFound
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
				return agg
			}(),
		},
		{
			name: "falls back to hydrating using the inner store the snapshot cannot be unmarshaled",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.HydrateOptions) error {
					aggregate.State().SetEntityAtVersion(aggregate.Entity(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      aggregateID,
						AggregateVersion: 42,
						Data:             []byte(`invalid json`),
					}, nil
				},
			},
			haveSnapshottingStoreOpts: []aggregatestore.SnapshottingStoreOption[*mockEntity]{
				aggregatestore.WithSnapshotMarshaler(estoria.JSONMarshaler[*mockEntity]{}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 45)
				return agg
			}(),
		},
		{
			name: "returns an error when the aggregate is nil",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate:     nil,
			wantErr:           aggregatestore.HydrateAggregateError{Err: aggregatestore.ErrNilAggregate},
		},
		{
			name: "returns an error when the target version is invalid",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			haveOpts: aggregatestore.HydrateOptions{
				ToVersion: -1,
			},
			wantErr: aggregatestore.HydrateAggregateError{Err: errors.New("invalid target version")},
		},
		{
			name: "returns an error when the snapshot stoe reader is nil",
			haveInner: &mockAggregateStore[*mockEntity]{
				NewFn: func(id uuid.UUID) (*aggregatestore.Aggregate[*mockEntity], error) {
					agg := &aggregatestore.Aggregate[*mockEntity]{}
					agg.State().SetEntityAtVersion(&mockEntity{ID: typeid.FromUUID("mockentity", id)}, 0)
					return agg, nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveSnapshottingStoreOpts: []aggregatestore.SnapshottingStoreOption[*mockEntity]{
				aggregatestore.WithSnapshotReader[*mockEntity](nil),
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: aggregatestore.HydrateAggregateError{Err: errors.New("snapshot store has no snapshot reader")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewSnapshottingStore(
				tt.haveInner,
				tt.haveSnapshotStore,
				snapshotstore.EventCountSnapshotPolicy{N: 1},
				tt.haveSnapshottingStoreOpts...,
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

func TestSnapshottingStore_Save(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)

	for _, tt := range []struct {
		name                      string
		haveInner                 aggregatestore.Store[*mockEntity]
		haveSnapshotStore         aggregatestore.SnapshotStore
		haveSnapshotPolicy        aggregatestore.SnapshotPolicy
		haveSnapshottingStoreOpts []aggregatestore.SnapshottingStoreOption[*mockEntity]
		haveAggregate             *aggregatestore.Aggregate[*mockEntity]
		haveOpts                  aggregatestore.SaveOptions
		wantAggregate             *aggregatestore.Aggregate[*mockEntity]
		wantErr                   error
	}{
		{
			name: "saves an aggregate using the inner store and creates no snapshot if the policy does not require it",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
						EntityEvent: &mockEntityEventA{},
					})
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{
				ShouldSnapshotFn: func(_ typeid.UUID, _ int64, _ time.Time) bool {
					return false
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{
					ID:         aggregateID,
					eventTypes: []estoria.EntityEvent{mockEntityEventA{}},
				}, 42)
				return agg
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 43)
				return agg
			}(),
		},
		{
			name: "saves an aggregate using the inner store and creates no snapshot if the snapshot fails to marshal",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
						EntityEvent: &mockEntityEventA{},
					})
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{
				ShouldSnapshotFn: func(_ typeid.UUID, _ int64, _ time.Time) bool {
					return true
				},
			},
			haveSnapshottingStoreOpts: []aggregatestore.SnapshottingStoreOption[*mockEntity]{
				aggregatestore.WithSnapshotMarshaler[*mockEntity](&mockSnapshotMarshaler{
					MarshalFn: func(_ **mockEntity) ([]byte, error) {
						return nil, errors.New("mock error")
					},
				}),
			},
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
			name: "saves an aggregate using the inner store and creates no snapshot if the snappshot writer fails to write",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
						EntityEvent: &mockEntityEventA{},
					})
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				WriteSnapshotFn: func(_ context.Context, _ *snapshotstore.AggregateSnapshot) error {
					return errors.New("mock error")
				},
			},
			haveSnapshotPolicy: &mockSnapshotPolicy{
				ShouldSnapshotFn: func(_ typeid.UUID, _ int64, _ time.Time) bool {
					return true
				},
			},
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
			name: "saves an aggregate using the inner store and creates a snapshot if the policy requires it",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
						EntityEvent: &mockEntityEventA{},
					})
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				WriteSnapshotFn: func(_ context.Context, _ *snapshotstore.AggregateSnapshot) error {
					return nil
				},
			},
			haveSnapshotPolicy: &mockSnapshotPolicy{
				ShouldSnapshotFn: func(_ typeid.UUID, _ int64, _ time.Time) bool {
					return true
				},
			},
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
			name:               "returns an error when the aggregate is nil",
			haveInner:          &mockAggregateStore[*mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveAggregate:      nil,
			wantErr:            aggregatestore.SaveAggregateError{Err: aggregatestore.ErrNilAggregate},
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, _ *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					return errors.New("mock error")
				},
			},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{ID: aggregateID}, 42)
				return agg
			}(),
			wantErr: aggregatestore.SaveAggregateError{Err: errors.New("saving aggregate using inner store: mock error")},
		},
		{
			name: "returns an error when encountering an unexpected error applying an event",
			haveInner: &mockAggregateStore[*mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[*mockEntity], _ aggregatestore.SaveOptions) error {
					aggregate.State().WillApply(&aggregatestore.AggregateEvent{
						EntityEvent: &mockEntityEventA{},
					})
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				WriteSnapshotFn: func(_ context.Context, _ *snapshotstore.AggregateSnapshot) error {
					return nil
				},
			},
			haveSnapshotPolicy: &mockSnapshotPolicy{
				ShouldSnapshotFn: func(_ typeid.UUID, _ int64, _ time.Time) bool {
					return true
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(&mockEntity{
					ID:            aggregateID,
					applyEventErr: errors.New("mock error"),
				}, 42)
				return agg
			}(),
			wantErr: aggregatestore.SaveAggregateError{Err: errors.New("applying next aggregate event: applying event: mock error")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewSnapshottingStore(tt.haveInner,
				tt.haveSnapshotStore,
				tt.haveSnapshotPolicy,
				tt.haveSnapshottingStoreOpts...,
			)
			if err != nil {
				t.Fatalf("unexpected error creating store: %v", err)
			} else if store == nil {
				t.Fatal("unexpected nil store")
			}

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
