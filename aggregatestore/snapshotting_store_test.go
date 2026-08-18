package aggregatestore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type mockSnapshotStore struct {
	ReadSnapshotFn  func(context.Context, typeid.ID, snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error)
	WriteSnapshotFn func(context.Context, *snapshotstore.AggregateSnapshot) error
}

func (m *mockSnapshotStore) ReadSnapshot(ctx context.Context, aggregateID typeid.ID, opts snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
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

// emptyFilteredReadIsNotFoundStore wraps an event store with the read semantics of backing
// stores that cannot distinguish an absent stream from a filtered read that matched nothing:
// a forward read with an AfterVersion at or beyond the stream tip is reported as
// ErrStreamNotFound. It counts those reads so a test can assert the case was exercised.
type emptyFilteredReadIsNotFoundStore struct {
	eventstore.Store
	emptyFilteredReads int
}

func (s *emptyFilteredReadIsNotFoundStore) ReadStream(ctx context.Context, id typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	iter, err := s.Store.ReadStream(ctx, id, opts)
	if err != nil {
		return nil, fmt.Errorf("reading stream: %w", err)
	}
	defer iter.Close(ctx)

	events, err := eventstore.Collect(ctx, iter)
	if err != nil {
		return nil, fmt.Errorf("reading events: %w", err)
	} else if len(events) == 0 {
		s.emptyFilteredReads++
		return nil, eventstore.ErrStreamNotFound
	}

	return &sliceStreamIterator{events: events}, nil
}

type sliceStreamIterator struct {
	events []*eventstore.Event
	cursor int
}

func (i *sliceStreamIterator) Next(_ context.Context) (*eventstore.Event, error) {
	if i.cursor >= len(i.events) {
		return nil, eventstore.ErrEndOfEventStream
	}

	event := i.events[i.cursor]
	i.cursor++
	return event, nil
}

func (i *sliceStreamIterator) Close(_ context.Context) error {
	return nil
}

type mockSnapshotPolicy struct {
	ShouldSnapshotFn func(typeid.ID, int64, time.Time) bool
}

func (m *mockSnapshotPolicy) ShouldSnapshot(aggregateID typeid.ID, version int64, timestamp time.Time) bool {
	if m.ShouldSnapshotFn != nil {
		return m.ShouldSnapshotFn(aggregateID, version, timestamp)
	}

	return false
}

type mockSnapshotMarshaler struct {
	MarshalFn   func(mockEntity) ([]byte, error)
	UnmarshalFn func([]byte, *mockEntity) error
}

func (m *mockSnapshotMarshaler) MarshalState(entity mockEntity) ([]byte, error) {
	if m.MarshalFn != nil {
		return m.MarshalFn(entity)
	}

	return nil, errors.New("unexpected call to Marshal")
}

func (m *mockSnapshotMarshaler) UnmarshalState(data []byte, entity *mockEntity) error {
	if m.UnmarshalFn != nil {
		return m.UnmarshalFn(data, entity)
	}

	return errors.New("unexpected call to Unmarshal")
}

func (m *mockSnapshotMarshaler) ContentType() string {
	return "application/x-mock"
}

func TestNewSnapshottingStore(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name               string
		haveInner          aggregatestore.Store[mockEntity]
		haveSnapshotStore  snapshotstore.SnapshotStore
		haveSnapshotPolicy aggregatestore.SnapshotPolicy
		haveOpts           []aggregatestore.SnapshottingStoreOption[mockEntity]
		wantErr            error
	}{
		{
			name:               "creates a new snapshotting store with default options",
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
		},
		{
			name:               "creates a new snapshotting store with a custom snapshot marshaler",
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[mockEntity]{
				aggregatestore.WithStateCodec(estoria.JSONStateCodec[mockEntity]{}),
			},
		},
		{
			name:               "creates a new snapshotting store with a custom snapshot reader",
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[mockEntity]{
				aggregatestore.WithSnapshotReader[mockEntity](&mockSnapshotStore{}),
			},
		},
		{
			name:               "creates a new snapshotting store with a custom snapshot writer",
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[mockEntity]{
				aggregatestore.WithSnapshotWriter[mockEntity](&mockSnapshotStore{}),
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
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  nil,
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			wantErr:            errors.New("snapshot store is required"),
		},
		{
			name:               "returns an error when the snapshot policy is nil",
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: nil,
			wantErr:            errors.New("snapshot policy is required"),
		},
		{
			name:               "returns an error when applying an option fails",
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveOpts: []aggregatestore.SnapshottingStoreOption[mockEntity]{
				func(*aggregatestore.SnapshottingStore[mockEntity]) error {
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

func TestSnapshottingStore_Load(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.Must(uuid.NewV4())

	for _, tt := range []struct {
		name                      string
		haveInner                 aggregatestore.Store[mockEntity]
		haveSnapshotStore         snapshotstore.SnapshotStore
		haveSnapshottingStoreOpts []aggregatestore.SnapshottingStoreOption[mockEntity]
		haveAggregateID           uuid.UUID
		haveOpts                  *aggregatestore.LoadOptions
		wantAggregate             *aggregatestore.Aggregate[mockEntity]
		wantErr                   error
	}{
		{
			name: "creates a new aggregate and hydrates it using default options",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 12,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregateID: aggregateID,
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 12)
			}(),
		},
		{
			name: "passes the correct ToVersion hydrate option",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity], opts *aggregatestore.HydrateOptions) error {
					if opts.ToVersion != 42 {
						return fmt.Errorf("want hydrate opts ToVersion 42, got %d", opts.ToVersion)
					}

					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 42,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregateID: aggregateID,
			haveOpts: &aggregatestore.LoadOptions{
				ToVersion: 42,
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when creating the aggregate fails",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					aggregate.TestOnlySetStateAtVersion(aggregate.State(), 42)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregateID:   aggregateID,
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when hydrating the aggregate fails",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					aggregate.TestOnlySetStateAtVersion(aggregate.State(), 42)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return nil, errors.New("mock error")
				},
			},
			haveAggregateID: aggregateID,
			haveOpts: &aggregatestore.LoadOptions{
				ToVersion: 42,
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID, 42)
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

func TestSnapshottingStore_Hydrate(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.NewV4("mockentity")

	for _, tt := range []struct {
		name                      string
		haveInner                 aggregatestore.Store[mockEntity]
		haveEntityFactory         estoria.StateFactory[mockEntity]
		haveSnapshotStore         snapshotstore.SnapshotStore
		haveSnapshottingStoreOpts []aggregatestore.SnapshottingStoreOption[mockEntity]
		haveAggregate             *aggregatestore.Aggregate[mockEntity]
		haveOpts                  *aggregatestore.HydrateOptions
		wantAggregate             *aggregatestore.Aggregate[mockEntity]
		wantErr                   error
	}{
		{
			name: "hydrates an aggregate to a snapshot version",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 42,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			}(),
			haveOpts: &aggregatestore.HydrateOptions{
				ToVersion: 42,
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
		},
		{
			name: "hydrates an aggregate to a snapshot version then further hydrates it using the inner store",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					aggregate.TestOnlySetStateAtVersion(aggregate.State(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, id typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      id,
						AggregateVersion: 42,
						Data:             []byte(`{"key":"value"}`),
					}, nil
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 45)
			}(),
		},
		// {
		// 	name: "passes the correct MaxVersion read snapshot option",
		// },
		{
			name: "falls back to hydrating using the inner store when already at the target version",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			haveOpts: &aggregatestore.HydrateOptions{
				ToVersion: 42,
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when target version is less than current version",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			haveOpts: &aggregatestore.HydrateOptions{
				ToVersion: 37,
			},
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when reading a snapshot fails",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					aggregate.TestOnlySetStateAtVersion(aggregate.State(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return nil, errors.New("mock error")
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 45)
			}(),
		},
		{
			name: "falls back to hydrating using the inner store when no snapshot is available",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					aggregate.TestOnlySetStateAtVersion(aggregate.State(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return nil, snapshotstore.ErrSnapshotNotFound
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 45)
			}(),
		},
		{
			name: "falls back to hydrating using the inner store the snapshot cannot be unmarshaled",
			haveInner: &mockAggregateStore[mockEntity]{
				NewFn: func(id uuid.UUID) *aggregatestore.Aggregate[mockEntity] {
					return newMockAggregate(id, 0)
				},
				HydrateFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.HydrateOptions) error {
					aggregate.TestOnlySetStateAtVersion(aggregate.State(), aggregate.Version()+3)
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{
				ReadSnapshotFn: func(_ context.Context, _ typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
					return &snapshotstore.AggregateSnapshot{
						AggregateID:      aggregateID,
						AggregateVersion: 42,
						Data:             []byte(`invalid json`),
					}, nil
				},
			},
			haveSnapshottingStoreOpts: []aggregatestore.SnapshottingStoreOption[mockEntity]{
				aggregatestore.WithStateCodec(estoria.JSONStateCodec[mockEntity]{}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 45)
			}(),
		},
		{
			name:              "returns an error when the aggregate is nil",
			haveInner:         &mockAggregateStore[mockEntity]{},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate:     nil,
			wantErr:           aggregatestore.HydrateError{Err: aggregatestore.ErrNilAggregate},
		},
		{
			name:              "returns an error when the target version is invalid",
			haveInner:         &mockAggregateStore[mockEntity]{},
			haveSnapshotStore: &mockSnapshotStore{},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			haveOpts: &aggregatestore.HydrateOptions{
				ToVersion: -1,
			},
			wantErr: aggregatestore.HydrateError{Err: errors.New("invalid target version")},
		},
		{
			name:              "returns an error when the snapshot stoe reader is nil",
			haveInner:         &mockAggregateStore[mockEntity]{},
			haveSnapshotStore: &mockSnapshotStore{},
			haveSnapshottingStoreOpts: []aggregatestore.SnapshottingStoreOption[mockEntity]{
				aggregatestore.WithSnapshotReader[mockEntity](nil),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantErr: aggregatestore.HydrateError{Err: errors.New("snapshot store has no snapshot reader")},
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
			if gotAggregate.ID().String() != typeid.New("mockentity", aggregateID.UUID).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.New("mockentity", aggregateID.UUID), gotAggregate.ID())
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

	aggregateID := typeid.NewV4("mockentity")

	for _, tt := range []struct {
		name                      string
		haveInner                 aggregatestore.Store[mockEntity]
		haveSnapshotStore         snapshotstore.SnapshotStore
		haveSnapshotPolicy        aggregatestore.SnapshotPolicy
		haveSnapshottingStoreOpts []aggregatestore.SnapshottingStoreOption[mockEntity]
		haveAggregate             *aggregatestore.Aggregate[mockEntity]
		haveOpts                  *aggregatestore.SaveOptions
		wantAggregate             *aggregatestore.Aggregate[mockEntity]
		wantErr                   error
	}{
		{
			name: "saves an aggregate using the inner store and creates no snapshot if the policy does not require it",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
						Version:     43,
						DomainEvent: mockEntityEventA{},
					})
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{
				ShouldSnapshotFn: func(_ typeid.ID, _ int64, _ time.Time) bool {
					return false
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 43)
			}(),
		},
		{
			name: "saves an aggregate using the inner store and creates no snapshot if the snapshot fails to marshal",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
						Version:     43,
						DomainEvent: mockEntityEventA{},
					})
					return nil
				},
			},
			haveSnapshotStore: &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{
				ShouldSnapshotFn: func(_ typeid.ID, _ int64, _ time.Time) bool {
					return true
				},
			},
			haveSnapshottingStoreOpts: []aggregatestore.SnapshottingStoreOption[mockEntity]{
				aggregatestore.WithStateCodec(&mockSnapshotMarshaler{
					MarshalFn: func(_ mockEntity) ([]byte, error) {
						return nil, errors.New("mock error")
					},
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 43)
			}(),
		},
		{
			name: "saves an aggregate using the inner store and creates no snapshot if the snapshot writer fails to write",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
						Version:     43,
						DomainEvent: mockEntityEventA{},
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
				ShouldSnapshotFn: func(_ typeid.ID, _ int64, _ time.Time) bool {
					return true
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 43)
			}(),
		},
		{
			name: "saves an aggregate using the inner store and creates a snapshot if the policy requires it",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
						Version:     43,
						DomainEvent: mockEntityEventA{},
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
				ShouldSnapshotFn: func(_ typeid.ID, _ int64, _ time.Time) bool {
					return true
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 43)
			}(),
		},
		{
			name:               "returns an error when the aggregate is nil",
			haveInner:          &mockAggregateStore[mockEntity]{},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveAggregate:      nil,
			wantErr:            aggregatestore.SaveError{Err: aggregatestore.ErrNilAggregate},
		},
		{
			name: "returns an error when the inner store returns an error",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, _ *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					return errors.New("mock error")
				},
			},
			haveSnapshotStore:  &mockSnapshotStore{},
			haveSnapshotPolicy: &mockSnapshotPolicy{},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantErr: aggregatestore.SaveError{Err: errors.New("saving aggregate using inner store: mock error")},
		},
		{
			// ApplyTo is total, so the apply loop can only fail when a queued event's
			// version disagrees with the aggregate's next version.
			name: "returns an error when a queued event is out of version order",
			haveInner: &mockAggregateStore[mockEntity]{
				SaveFn: func(_ context.Context, aggregate *aggregatestore.Aggregate[mockEntity], _ *aggregatestore.SaveOptions) error {
					aggregate.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
						Version:     45,
						DomainEvent: mockEntityEventA{},
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
				ShouldSnapshotFn: func(_ typeid.ID, _ int64, _ time.Time) bool {
					return true
				},
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 42)
			}(),
			wantErr: aggregatestore.SaveError{Err: errors.New("applying next aggregate event: events appended but not applied to the aggregate: event version mismatch: expected 43, got 45")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewSnapshottingStore(
				tt.haveInner,
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
			if gotAggregate.ID().String() != typeid.New("mockentity", aggregateID.UUID).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.New("mockentity", aggregateID.UUID), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantAggregate.Version() {
				t.Errorf("want aggregate version %d, got %d", tt.wantAggregate.Version(), gotAggregate.Version())
			}
		})
	}
}

// TestSnapshottingStore_LoadsAggregateSnapshottedAtStreamTip is a regression test for
// https://github.com/go-estoria/estoria/issues/24: when a snapshot lands on exactly the
// stream tip, hydrating from it leaves the inner store reading past the end of the stream.
// Event stores that report such a read as ErrStreamNotFound made the aggregate look like it
// did not exist, once every N events, until the next write moved the tip past the snapshot.
func TestSnapshottingStore_LoadsAggregateSnapshottedAtStreamTip(t *testing.T) {
	t.Parallel()

	const snapshotEvery = 3

	ctx := t.Context()
	aggregateID := uuid.Must(uuid.NewV4())

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("unexpected error creating event store: %v", err)
	}

	wrappedEventStore := &emptyFilteredReadIsNotFoundStore{Store: eventStore}

	inner, err := aggregatestore.New(wrappedEventStore, "mockentity", newMockEntity,
		aggregatestore.WithEventTypes(mockEntityEventA{}),
	)
	if err != nil {
		t.Fatalf("unexpected error creating inner store: %v", err)
	}

	var snapshot *snapshotstore.AggregateSnapshot

	store, err := aggregatestore.NewSnapshottingStore[mockEntity](
		inner,
		&mockSnapshotStore{
			ReadSnapshotFn: func(_ context.Context, _ typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
				if snapshot == nil {
					return nil, snapshotstore.ErrSnapshotNotFound
				}

				return snapshot, nil
			},
			WriteSnapshotFn: func(_ context.Context, snap *snapshotstore.AggregateSnapshot) error {
				snapshot = snap
				return nil
			},
		},
		&mockSnapshotPolicy{
			ShouldSnapshotFn: func(_ typeid.ID, version int64, _ time.Time) bool {
				return version%snapshotEvery == 0
			},
		},
		// round-trips the entity state that JSON cannot see, so that a load from a snapshot
		// is distinguishable from a full replay of the stream
		aggregatestore.WithStateCodec[mockEntity](&mockSnapshotMarshaler{
			MarshalFn: func(entity mockEntity) ([]byte, error) {
				return []byte(strconv.FormatInt(entity.numAppliedEvents, 10)), nil
			},
			UnmarshalFn: func(data []byte, entity *mockEntity) error {
				numAppliedEvents, err := strconv.ParseInt(string(data), 10, 64)
				if err != nil {
					return fmt.Errorf("parsing applied event count: %w", err)
				}

				entity.numAppliedEvents = numAppliedEvents
				return nil
			},
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error creating store: %v", err)
	}

	aggregate := store.New(aggregateID)

	// save one event at a time, loading after each save, so the aggregate is read back at
	// every version -- including the versions where a snapshot lands on the stream tip
	for version := int64(1); version <= 2*snapshotEvery; version++ {
		aggregate.Append(mockEntityEventA{A: "a"})
		if err := store.Save(ctx, aggregate, nil); err != nil {
			t.Fatalf("unexpected error saving aggregate at version %d: %v", version, err)
		}

		loaded, err := store.Load(ctx, aggregateID, nil)
		if err != nil {
			t.Fatalf("unexpected error loading aggregate at version %d: %v", version, err)
		}

		if loaded.Version() != version {
			t.Errorf("want aggregate version %d, got %d", version, loaded.Version())
		}

		if got := loaded.State().numAppliedEvents; got != version {
			t.Errorf("want %d applied events at version %d, got %d", version, version, got)
		}
	}

	// the loads at versions 3 and 6 hydrate from a snapshot taken at the stream tip, leaving
	// the inner store to read past the end of the stream; without those reads happening, the
	// loads above would succeed whether or not the bug is fixed
	if want := 2; wrappedEventStore.emptyFilteredReads != want {
		t.Errorf("want %d empty filtered reads, got %d", want, wrappedEventStore.emptyFilteredReads)
	}
}

// mockPointerEntity is a pointer-typed entity. The rest of this suite uses the value-typed
// mockEntity, which is why the in-place snapshot corruption below went unnoticed: a marshaler
// writing through *E only touches the aggregate's live entity when E is a pointer.
type mockPointerEntity struct {
	ID typeid.ID `json:"id"`
	// Balance is advanced by events.
	Balance int `json:"balance"`
	// Owner is only ever set by a snapshot, never by an event, so any value here after a
	// failed snapshot load is state that leaked out of a partial unmarshal.
	Owner string `json:"owner"`
}

func newMockPointerEntity(id uuid.UUID) *mockPointerEntity {
	return &mockPointerEntity{ID: typeid.New("mockpointerentity", id)}
}

func (e *mockPointerEntity) EntityID() typeid.ID { return e.ID }

type mockPointerEntityEvent struct {
	Amount int `json:"amount"`
}

func (e *mockPointerEntityEvent) EventType() string { return "credited" }

func (e *mockPointerEntityEvent) New() estoria.DomainEvent[*mockPointerEntity] {
	return &mockPointerEntityEvent{}
}

func (e *mockPointerEntityEvent) ApplyTo(entity *mockPointerEntity) *mockPointerEntity {
	entity.Balance += e.Amount
	return entity
}

// TestSnapshottingStore_Save_DoesNotMutateCallerOptions guards against Save setting
// SkipApply on the caller's own SaveOptions. Leaking it meant the next save that reused the
// struct silently skipped applying events, leaving the aggregate's in-memory version stale,
// which surfaced one save later as a spurious StreamVersionMismatchError.
func TestSnapshottingStore_Save_DoesNotMutateCallerOptions(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	inner, err := aggregatestore.New[mockEntity](eventStore, "mockentity", newMockEntity,
		aggregatestore.WithEventTypes[mockEntity](&mockEntityEventA{}))
	if err != nil {
		t.Fatalf("creating inner store: %v", err)
	}

	snapshotting, err := aggregatestore.NewSnapshottingStore[mockEntity](
		inner,
		&mockSnapshotStore{
			WriteSnapshotFn: func(context.Context, *snapshotstore.AggregateSnapshot) error { return nil },
		},
		&mockSnapshotPolicy{ShouldSnapshotFn: func(typeid.ID, int64, time.Time) bool { return false }},
	)
	if err != nil {
		t.Fatalf("creating snapshotting store: %v", err)
	}

	opts := &aggregatestore.SaveOptions{}

	first := snapshotting.New(uuid.Must(uuid.NewV4()))
	first.Append(&mockEntityEventA{})
	if err := snapshotting.Save(t.Context(), first, opts); err != nil {
		t.Fatalf("saving through snapshotting store: %v", err)
	}

	if opts.SkipApply {
		t.Error("Save mutated the caller's SaveOptions: want SkipApply false, got true")
	}

	// Reusing the same options on another store must still apply events.
	second := inner.New(uuid.Must(uuid.NewV4()))
	second.Append(&mockEntityEventA{})
	if err := inner.Save(t.Context(), second, opts); err != nil {
		t.Fatalf("saving through inner store: %v", err)
	}

	if want := int64(1); second.Version() != want {
		t.Errorf("want version %d after reusing options, got %d", want, second.Version())
	}

	// The stale version is what produced the spurious conflict, so save once more.
	second.Append(&mockEntityEventA{})
	if err := inner.Save(t.Context(), second, &aggregatestore.SaveOptions{}); err != nil {
		t.Errorf("unexpected error on subsequent save: %v", err)
	}
}

// TestSnapshottingStore_Hydrate_PartialSnapshotDoesNotCorruptEntity guards against a
// snapshot that is syntactically valid JSON but disagrees on a field's type — ordinary
// schema drift — writing the fields it can into the aggregate's live entity before failing.
// The store falls back to full hydration on unmarshal failure, so any leaked state would be
// replayed on top of and returned with a nil error.
//
// Note: truncated JSON does NOT reproduce this. encoding/json validates the whole document
// before writing, so the payload has to parse cleanly and fail on a type mismatch.
func TestSnapshottingStore_Hydrate_PartialSnapshotDoesNotCorruptEntity(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	inner, err := aggregatestore.New[*mockPointerEntity](eventStore, "mockpointerentity", newMockPointerEntity,
		aggregatestore.WithEventTypes[*mockPointerEntity](&mockPointerEntityEvent{}))
	if err != nil {
		t.Fatalf("creating inner store: %v", err)
	}

	// Seed a stream: three credits of one each.
	id := uuid.Must(uuid.NewV4())
	seed := inner.New(id)
	for range 3 {
		seed.Append(&mockPointerEntityEvent{Amount: 1})
	}
	if err := inner.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding stream: %v", err)
	}

	snapshotting, err := aggregatestore.NewSnapshottingStore[*mockPointerEntity](
		inner,
		&mockSnapshotStore{
			ReadSnapshotFn: func(_ context.Context, aggregateID typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
				return &snapshotstore.AggregateSnapshot{
					AggregateID:      aggregateID,
					AggregateVersion: 2,
					// Parses cleanly; owner lands, then balance fails on type.
					Data: []byte(`{"owner":"corrupted","balance":"9999"}`),
				}, nil
			},
		},
		&mockSnapshotPolicy{ShouldSnapshotFn: func(typeid.ID, int64, time.Time) bool { return false }},
	)
	if err != nil {
		t.Fatalf("creating snapshotting store: %v", err)
	}

	got, err := snapshotting.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading aggregate: %v", err)
	}

	if want := int64(3); got.Version() != want {
		t.Errorf("want version %d, got %d", want, got.Version())
	}
	if want := 3; got.State().Balance != want {
		t.Errorf("want balance %d, got %d", want, got.State().Balance)
	}
	if owner := got.State().Owner; owner != "" {
		t.Errorf("state leaked from a failed snapshot unmarshal: want empty owner, got %q", owner)
	}
}

// validatedEntity is a state type that vouches for its snapshots: payloads
// marked Fabricated are ones no fold produces.
type validatedEntity struct {
	Applied    int
	Fabricated bool
}

func (e validatedEntity) ValidateSnapshotState() error {
	if e.Fabricated {
		return errors.New("fabricated state")
	}

	return nil
}

type validatedEntityEvent struct{}

func (validatedEntityEvent) EventType() string { return "validatedentityevent" }

func (validatedEntityEvent) New() estoria.DomainEvent[validatedEntity] {
	return &validatedEntityEvent{}
}

func (validatedEntityEvent) ApplyTo(s validatedEntity) validatedEntity {
	s.Applied++
	return s
}

// pointerValidatedEntity is validatedEntity's pointer-state twin: the store's
// state type is *pointerValidatedEntity and the validator reads through its
// receiver, so a typed-nil state decoded from a null payload panics if it is
// ever consulted.
type pointerValidatedEntity struct {
	Applied    int
	Fabricated bool
}

func (e *pointerValidatedEntity) ValidateSnapshotState() error {
	if e.Fabricated {
		return errors.New("fabricated state")
	}

	return nil
}

type pointerValidatedEntityEvent struct{}

func (pointerValidatedEntityEvent) EventType() string { return "pointervalidatedentityevent" }

func (pointerValidatedEntityEvent) New() estoria.DomainEvent[*pointerValidatedEntity] {
	return &pointerValidatedEntityEvent{}
}

func (pointerValidatedEntityEvent) ApplyTo(s *pointerValidatedEntity) *pointerValidatedEntity {
	s.Applied++
	return s
}

// addressValidatedEntity declares its validator on the pointer receiver while
// the store's state type is the value: only the decoded state's address
// satisfies the interface.
type addressValidatedEntity struct {
	Applied    int
	Fabricated bool
}

func (e *addressValidatedEntity) ValidateSnapshotState() error {
	if e.Fabricated {
		return errors.New("fabricated state")
	}

	return nil
}

type addressValidatedEntityEvent struct{}

func (addressValidatedEntityEvent) EventType() string { return "addressvalidatedentityevent" }

func (addressValidatedEntityEvent) New() estoria.DomainEvent[addressValidatedEntity] {
	return &addressValidatedEntityEvent{}
}

func (addressValidatedEntityEvent) ApplyTo(s addressValidatedEntity) addressValidatedEntity {
	s.Applied++
	return s
}

// plainPointerEntity has no validator at all: the nil-state guard must
// protect it anyway, or a null payload installs a nil state that the first
// tail event dereferences.
type plainPointerEntity struct {
	Applied int
}

type plainPointerEntityEvent struct{}

func (plainPointerEntityEvent) EventType() string { return "plainpointerentityevent" }

func (plainPointerEntityEvent) New() estoria.DomainEvent[*plainPointerEntity] {
	return &plainPointerEntityEvent{}
}

func (plainPointerEntityEvent) ApplyTo(s *plainPointerEntity) *plainPointerEntity {
	s.Applied++
	return s
}

// mapEntityEvent folds map state by assignment, the shape that panics if a
// nil map is ever installed beneath it.
type mapEntityEvent struct{}

func (mapEntityEvent) EventType() string { return "mapentityevent" }

func (mapEntityEvent) New() estoria.DomainEvent[map[string]int] { return &mapEntityEvent{} }

func (mapEntityEvent) ApplyTo(s map[string]int) map[string]int {
	s["applied"]++
	return s
}

type sliceEntityEvent struct{}

func (sliceEntityEvent) EventType() string { return "sliceentityevent" }

func (sliceEntityEvent) New() estoria.DomainEvent[[]int] { return &sliceEntityEvent{} }

func (sliceEntityEvent) ApplyTo(s []int) []int { return append(s, 1) }

// entityIface is an interface state type: assertions on the state and on
// its address see only the interface, so the dynamic value's
// pointer-receiver validator is reachable only through an addressable copy.
type entityIface interface{ isEntityIface() }

type ifaceValidatedEntity struct {
	Applied    int
	Fabricated bool
	Vouched    bool
}

func (ifaceValidatedEntity) isEntityIface() {}

// ValidateSnapshotState marks the copy it accepts, so a test can prove the
// installed state is the exact value the validator vouched for.
func (e *ifaceValidatedEntity) ValidateSnapshotState() error {
	if e.Fabricated {
		return errors.New("fabricated state")
	}

	e.Vouched = true

	return nil
}

type ifaceEntityEvent struct{}

func (ifaceEntityEvent) EventType() string { return "ifaceentityevent" }

func (ifaceEntityEvent) New() estoria.DomainEvent[entityIface] { return &ifaceEntityEvent{} }

func (ifaceEntityEvent) ApplyTo(s entityIface) entityIface {
	c, _ := s.(ifaceValidatedEntity)
	c.Applied++

	return c
}

// ifaceEntityCodec decodes into the concrete type and assigns it to the
// interface; the stock JSON codec cannot decode into an interface.
type ifaceEntityCodec struct{}

func (ifaceEntityCodec) MarshalState(s entityIface) ([]byte, error) { return json.Marshal(s) }

func (ifaceEntityCodec) UnmarshalState(data []byte, dest *entityIface) error {
	var c ifaceValidatedEntity
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}

	*dest = c

	return nil
}

func (ifaceEntityCodec) ContentType() string { return estoria.ContentTypeJSON }

// nilingMapEntity is a named map whose pointer-receiver validator accepts
// while nilling the very state it vouches for: unless nil is rechecked after
// validation, the store installs nil and the first tail event panics on map
// assignment.
type nilingMapEntity map[string]int

func (m *nilingMapEntity) ValidateSnapshotState() error {
	*m = nil
	return nil
}

type nilingMapEntityEvent struct{}

func (nilingMapEntityEvent) EventType() string { return "nilingmapentityevent" }

func (nilingMapEntityEvent) New() estoria.DomainEvent[nilingMapEntity] {
	return &nilingMapEntityEvent{}
}

func (nilingMapEntityEvent) ApplyTo(s nilingMapEntity) nilingMapEntity {
	s["applied"]++
	return s
}

// newValidatedSnapshotStore seeds a three-event stream for a state type that
// implements SnapshotStateValidator and wraps its store in a snapshotting
// store whose snapshot store serves snapshotData at version 2.
func newValidatedSnapshotStore[S any](
	t *testing.T,
	streamType string,
	factory func(uuid.UUID) S,
	event estoria.DomainEvent[S],
	snapshotData []byte,
	opts ...aggregatestore.SnapshottingStoreOption[S],
) *aggregatestore.SnapshottingStore[S] {
	t.Helper()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	inner, err := aggregatestore.New(eventStore, streamType, factory,
		aggregatestore.WithEventTypes[S](event))
	if err != nil {
		t.Fatalf("creating inner store: %v", err)
	}

	seed := inner.New(uuid.NewV5(uuid.NamespaceOID, streamType))
	seed.Append(event, event, event)

	if err := inner.Save(t.Context(), seed, nil); err != nil {
		t.Fatalf("seeding stream: %v", err)
	}

	snapshotting, err := aggregatestore.NewSnapshottingStore(
		inner,
		&mockSnapshotStore{
			ReadSnapshotFn: func(_ context.Context, aggregateID typeid.ID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
				return &snapshotstore.AggregateSnapshot{
					AggregateID:      aggregateID,
					AggregateVersion: 2,
					Data:             snapshotData,
				}, nil
			},
		},
		&mockSnapshotPolicy{ShouldSnapshotFn: func(typeid.ID, int64, time.Time) bool { return false }},
		opts...,
	)
	if err != nil {
		t.Fatalf("creating snapshotting store: %v", err)
	}

	return snapshotting
}

// TestSnapshottingStore_Hydrate_ValidatesSnapshotState pins the
// SnapshotStateValidator contract: a decoded payload the state type rejects
// is skipped exactly like an undecodable one — the aggregate hydrates fully
// from its events and the fabricated state is never installed — while an
// accepted payload is installed and only the tail is folded on top. The
// validator is honored on whichever receiver form declares it, and a null
// payload for a pointer state type falls back to full hydration rather than
// reaching the validator or the aggregate as a typed nil.
func TestSnapshottingStore_Hydrate_ValidatesSnapshotState(t *testing.T) {
	t.Parallel()

	newValueStore := func(t *testing.T, snapshotData []byte) *aggregatestore.SnapshottingStore[validatedEntity] {
		t.Helper()
		return newValidatedSnapshotStore(t, "validatedentity",
			func(uuid.UUID) validatedEntity { return validatedEntity{} },
			validatedEntityEvent{}, snapshotData)
	}

	newPointerStore := func(t *testing.T, snapshotData []byte) *aggregatestore.SnapshottingStore[*pointerValidatedEntity] {
		t.Helper()
		return newValidatedSnapshotStore(t, "pointervalidatedentity",
			func(uuid.UUID) *pointerValidatedEntity { return &pointerValidatedEntity{} },
			pointerValidatedEntityEvent{}, snapshotData)
	}

	newAddressStore := func(t *testing.T, snapshotData []byte) *aggregatestore.SnapshottingStore[addressValidatedEntity] {
		t.Helper()
		return newValidatedSnapshotStore(t, "addressvalidatedentity",
			func(uuid.UUID) addressValidatedEntity { return addressValidatedEntity{} },
			addressValidatedEntityEvent{}, snapshotData)
	}

	t.Run("rejected payload falls back to full hydration", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValueStore(t, []byte(`{"Applied":99,"Fabricated":true}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "validatedentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state.Applied != 3 || state.Fabricated {
			t.Errorf("want the full replay of 3 events with nothing installed from the snapshot, got %+v", state)
		}
	})

	t.Run("accepted payload is installed", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValueStore(t, []byte(`{"Applied":50}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "validatedentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state.Applied != 51 {
			t.Errorf("want the snapshot installed at version 2 with one tail event folded, got %+v", state)
		}
	})

	t.Run("null payload for pointer state falls back instead of panicking", func(t *testing.T) {
		t.Parallel()

		snapshotting := newPointerStore(t, []byte(`null`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "pointervalidatedentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		state := got.State()
		if state == nil {
			t.Fatal("want the full replay, got the null snapshot's nil state installed")
		}

		if state.Applied != 3 {
			t.Errorf("want the full replay of 3 events, got %+v", state)
		}
	})

	t.Run("valid payload for pointer state is installed", func(t *testing.T) {
		t.Parallel()

		snapshotting := newPointerStore(t, []byte(`{"Applied":50}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "pointervalidatedentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state == nil || state.Applied != 51 {
			t.Errorf("want the snapshot installed at version 2 with one tail event folded, got %+v", state)
		}
	})

	t.Run("pointer-receiver validator rejects through the value state's address", func(t *testing.T) {
		t.Parallel()

		snapshotting := newAddressStore(t, []byte(`{"Applied":99,"Fabricated":true}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "addressvalidatedentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state.Applied != 3 || state.Fabricated {
			t.Errorf("want the pointer-receiver validator found and the fabricated payload replaced by the full replay, got %+v", state)
		}
	})

	t.Run("pointer-receiver validator accepts through the value state's address", func(t *testing.T) {
		t.Parallel()

		snapshotting := newAddressStore(t, []byte(`{"Applied":50}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "addressvalidatedentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state.Applied != 51 {
			t.Errorf("want the snapshot installed at version 2 with one tail event folded, got %+v", state)
		}
	})

	t.Run("fabricated payload for pointer state is rejected", func(t *testing.T) {
		t.Parallel()

		snapshotting := newPointerStore(t, []byte(`{"Applied":99,"Fabricated":true}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "pointervalidatedentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state.Applied != 3 || state.Fabricated {
			t.Errorf("want the fabricated payload replaced by the full replay, got %+v", state)
		}
	})

	t.Run("null payload without a validator falls back", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "plainpointerentity",
			func(uuid.UUID) *plainPointerEntity { return &plainPointerEntity{} },
			plainPointerEntityEvent{}, []byte(`null`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "plainpointerentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		state := got.State()
		if state == nil || state.Applied != 3 {
			t.Errorf("want the full replay of 3 events, got %+v", state)
		}
	})

	t.Run("null payload for map state falls back instead of installing nil", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "mapentity",
			func(uuid.UUID) map[string]int { return map[string]int{} },
			mapEntityEvent{}, []byte(`null`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "mapentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state["applied"] != 3 {
			t.Errorf("want the full replay of 3 events, got %+v", state)
		}
	})

	t.Run("valid payload for map state is installed", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "mapentity",
			func(uuid.UUID) map[string]int { return map[string]int{} },
			mapEntityEvent{}, []byte(`{"applied":50}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "mapentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state["applied"] != 51 {
			t.Errorf("want the snapshot installed at version 2 with one tail event folded, got %+v", state)
		}
	})

	t.Run("null payload for slice state falls back", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "sliceentity",
			func(uuid.UUID) []int { return []int{} },
			sliceEntityEvent{}, []byte(`null`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "sliceentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); len(state) != 3 {
			t.Errorf("want the full replay of 3 events, got %+v", state)
		}
	})

	t.Run("pointer-receiver validator on an interface state's dynamic value rejects", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "ifaceentity",
			func(uuid.UUID) entityIface { return ifaceValidatedEntity{} },
			ifaceEntityEvent{}, []byte(`{"Applied":99,"Fabricated":true}`),
			aggregatestore.WithStateCodec[entityIface](ifaceEntityCodec{}))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "ifaceentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		state, ok := got.State().(ifaceValidatedEntity)
		if !ok || state.Applied != 3 || state.Fabricated {
			t.Errorf("want the fabricated payload replaced by the full replay, got %+v", got.State())
		}
	})

	t.Run("interface state accepted payload installs the validated copy", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "ifaceentity",
			func(uuid.UUID) entityIface { return ifaceValidatedEntity{} },
			ifaceEntityEvent{}, []byte(`{"Applied":50}`),
			aggregatestore.WithStateCodec[entityIface](ifaceEntityCodec{}))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "ifaceentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		state, ok := got.State().(ifaceValidatedEntity)
		if !ok || state.Applied != 51 || !state.Vouched {
			t.Errorf("want the copy the validator vouched for installed at version 2 with one tail event folded, got %+v", got.State())
		}
	})

	t.Run("valid payload for slice state is installed", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "sliceentity",
			func(uuid.UUID) []int { return []int{} },
			sliceEntityEvent{}, []byte(`[5,5]`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "sliceentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		state := got.State()
		if len(state) != 3 || state[0] != 5 || state[1] != 5 || state[2] != 1 {
			t.Errorf("want the snapshot installed at version 2 with one tail event appended, got %+v", state)
		}
	})

	t.Run("state nilled by an accepting validator falls back", func(t *testing.T) {
		t.Parallel()

		snapshotting := newValidatedSnapshotStore(t, "nilingmapentity",
			func(uuid.UUID) nilingMapEntity { return nilingMapEntity{} },
			nilingMapEntityEvent{}, []byte(`{"applied":50}`))

		got, err := snapshotting.Load(t.Context(), uuid.NewV5(uuid.NamespaceOID, "nilingmapentity"), nil)
		if err != nil {
			t.Fatalf("loading aggregate: %v", err)
		}

		if state := got.State(); state == nil || state["applied"] != 3 {
			t.Errorf("want the full replay of 3 events, got %+v", state)
		}
	})
}

// TestNilState pins the nil guard's coverage directly — every nilable kind
// is rejected when nil, and nothing else is — so the "every nilable kind"
// claim behind the null-payload fallbacks holds by enumeration rather than
// by the store harnesses alone.
func TestNilState(t *testing.T) {
	t.Parallel()

	var (
		nilChan   chan int
		nilFunc   func()
		nilIface  error
		nilMap    map[string]int
		nilPtr    *int
		nilSlice  []int
		nilUnsafe unsafe.Pointer
	)

	typedNilInIface := error((*strconv.NumError)(nil))
	n := 5

	for _, tt := range []struct {
		name  string
		state any
		want  bool
	}{
		{name: "nil interface", state: nilIface, want: true},
		{name: "typed nil pointer in an interface", state: typedNilInIface, want: true},
		{name: "nil channel", state: nilChan, want: true},
		{name: "nil function", state: nilFunc, want: true},
		{name: "nil map", state: nilMap, want: true},
		{name: "nil pointer", state: nilPtr, want: true},
		{name: "nil slice", state: nilSlice, want: true},
		{name: "nil unsafe pointer", state: nilUnsafe, want: true},
		{name: "non-nil channel", state: make(chan int), want: false},
		{name: "non-nil function", state: func() {}, want: false},
		{name: "non-nil map", state: map[string]int{}, want: false},
		{name: "non-nil pointer", state: &n, want: false},
		{name: "non-nil slice", state: []int{}, want: false},
		{name: "non-nil unsafe pointer", state: unsafe.Pointer(&n), want: false},
		{name: "integer zero", state: 0, want: false},
		{name: "struct value", state: struct{}{}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := aggregatestore.NilStateForTest(tt.state); got != tt.want {
				t.Errorf("want %v, got %v", tt.want, got)
			}
		})
	}
}
