package aggregatestore_test

import (
	"context"
	"errors"
	"log"
	"testing"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type eventSourcedStoreTestCase[E estoria.Entity] struct {
	name              string
	haveEventStore    func() eventstore.Store
	haveEntityFactory estoria.EntityFactory[E]
	haveOpts          []aggregatestore.EventSourcedStoreOption[E]
	wantErr           error
}

type mockEntityEventA struct{}

func (e mockEntityEventA) EventType() string {
	return "mockEntityEventA"
}

func (e mockEntityEventA) New() estoria.EntityEvent {
	return &mockEntityEventA{}
}

func (e mockEntityEventA) marshaledData() []byte {
	b, _ := estoria.JSONMarshaler[mockEntityEventA]{}.Marshal(&e)
	return b
}

type mockEntityEventB struct{}

func (e mockEntityEventB) EventType() string {
	return "mockEntityEventB"
}

func (e mockEntityEventB) New() estoria.EntityEvent {
	return &mockEntityEventB{}
}

func (e mockEntityEventB) marshaledData() []byte {
	b, _ := estoria.JSONMarshaler[mockEntityEventB]{}.Marshal(&e)
	return b
}

type mockEntityEventC struct{}

func (e mockEntityEventC) EventType() string {
	return "mockEntityEventC"
}

func (e mockEntityEventC) New() estoria.EntityEvent {
	return &mockEntityEventC{}
}

func (e mockEntityEventC) marshaledData() []byte {
	b, _ := estoria.JSONMarshaler[mockEntityEventC]{}.Marshal(&e)
	return b
}

type mockEntityEventD struct{}

func (e mockEntityEventD) EventType() string {
	return "mockEntityEventD"
}

func (e mockEntityEventD) New() estoria.EntityEvent {
	return &mockEntityEventD{}
}

func (e mockEntityEventD) marshaledData() []byte {
	b, _ := estoria.JSONMarshaler[mockEntityEventD]{}.Marshal(&e)
	return b
}

type mockEntityEventE struct{}

func (e mockEntityEventE) EventType() string {
	return "mockEntityEventE"
}

func (e mockEntityEventE) New() estoria.EntityEvent {
	return &mockEntityEventE{}
}

func (e mockEntityEventE) marshaledData() []byte {
	b, _ := estoria.JSONMarshaler[mockEntityEventE]{}.Marshal(&e)
	return b
}

type mockStreamReader struct {
	readStreamIterator eventstore.StreamIterator
	readStreamErr      error
}

func (m mockStreamReader) ReadStream(_ context.Context, _ typeid.UUID, _ eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	return m.readStreamIterator, m.readStreamErr
}

type mockStreamWriter struct {
	appendStreamErr error
}

func (m mockStreamWriter) AppendStream(_ context.Context, _ typeid.UUID, _ []*eventstore.WritableEvent, _ eventstore.AppendStreamOptions) error {
	return m.appendStreamErr
}

type mockStreamIterator struct {
	nextEvent *eventstore.Event
	nextErr   error
	closeErr  error
}

func (m mockStreamIterator) Next(_ context.Context) (*eventstore.Event, error) {
	return m.nextEvent, m.nextErr
}

func (m mockStreamIterator) Close(_ context.Context) error {
	return m.closeErr
}

type mockEventMarshaler struct {
	marshaledBytes []byte
	marshalErr     error
	unmarshalErr   error
}

func (m mockEventMarshaler) Marshal(_ *estoria.EntityEvent) ([]byte, error) {
	return m.marshaledBytes, m.marshalErr
}

func (m mockEventMarshaler) Unmarshal(_ []byte, _ *estoria.EntityEvent) error {
	log.Println("UNMARSHAL")
	return m.unmarshalErr
}

func TestNewEventSourcedStore(t *testing.T) {
	t.Parallel()

	for _, tt := range []eventSourcedStoreTestCase[*mockEntity]{
		{
			name: "creates a new event sourced store with default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{ID: typeid.FromUUID("testentity", id)}
			},
		},
		{
			name: "creates a new event sourced store with default options and registers entity event prototypes",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{
					ID: typeid.FromUUID("testentity", id),
					eventTypes: []estoria.EntityEvent{
						mockEntityEventA{},
						mockEntityEventB{},
						mockEntityEventC{},
					},
				}
			},
		},
		{
			name: "creates a new event sourced store with a custom entity event marshaler",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{ID: typeid.FromUUID("testentity", id)}
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventMarshaler[*mockEntity](mockEventMarshaler{}),
			},
		},
		{
			name: "creates a new event sourced store with a custom stream reader",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{ID: typeid.FromUUID("testentity", id)}
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamReader[*mockEntity](func() eventstore.Store {
					store, _ := memory.NewEventStore()
					return store
				}()),
			},
		},
		{
			name: "creates a new event sourced store with a custom stream writer",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{ID: typeid.FromUUID("testentity", id)}
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamWriter[*mockEntity](func() eventstore.Store {
					store, _ := memory.NewEventStore()
					return store
				}()),
			},
		},
		{
			name: "returns an error when no event store is provided",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{ID: typeid.FromUUID("testentity", id)}
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamReader[*mockEntity](func() eventstore.Store {
					return nil
				}()),
				aggregatestore.WithEventStreamWriter[*mockEntity](func() eventstore.Store {
					return nil
				}()),
			},
			wantErr: aggregatestore.InitializeAggregateStoreError{Err: errors.New("no event stream reader or writer provided")},
		},
		{
			name: "returns an error when a duplicate entity event prototype is registered",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{
					ID: typeid.FromUUID("testentity", id),
					eventTypes: []estoria.EntityEvent{
						mockEntityEventA{},
						mockEntityEventB{},
						mockEntityEventA{},
					},
				}
			},
			wantErr: aggregatestore.InitializeAggregateStoreError{
				Operation: "registering entity event prototype",
				Err:       errors.New("duplicate event type mockEntityEventA for entity testentity"),
			},
		},
		{
			name: "returns an error when applying an option fails",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return &mockEntity{ID: typeid.FromUUID("testentity", id)}
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				func(_ *aggregatestore.EventSourcedStore[*mockEntity]) error {
					return errors.New("test error")
				},
			},
			wantErr: aggregatestore.InitializeAggregateStoreError{
				Operation: "applying option",
				Err:       errors.New("test error"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStore, err := aggregatestore.NewEventSourcedStore(tt.haveEventStore(), tt.haveEntityFactory, tt.haveOpts...)

			if tt.wantErr != nil {
				if err == nil || err.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error %v", err)
			} else if gotStore == nil {
				t.Errorf("unexpected nil store")
			}
		})
	}
}

func TestEventSourcedStore_New(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		haveUUID uuid.UUID
		wantErr  error
	}{
		{
			name:     "creates a new aggregate with the provided ID",
			haveUUID: uuid.Must(uuid.NewV4()),
		},
		{
			name:     "returns an error when provided a nil aggregate ID",
			haveUUID: uuid.Nil,
			wantErr:  aggregatestore.CreateAggregateError{Err: errors.New("aggregate ID is nil")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewEventSourcedStore(
				func() eventstore.Store {
					store, _ := memory.NewEventStore()
					return store
				}(),
				func(id uuid.UUID) *mockEntity {
					return &mockEntity{ID: typeid.FromUUID("testentity", id)}
				})
			if err != nil {
				t.Errorf("unexpected error creating store: %v", err)
			}

			gotAggregate, err := store.New(tt.haveUUID)

			if tt.wantErr != nil {
				if err == nil || err.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			} else if gotAggregate == nil {
				t.Errorf("unexpected nil aggregate")
			}

			// aggregate has the correct ID
			if gotAggregate.ID().String() != typeid.FromUUID("testentity", tt.haveUUID).String() {
				t.Errorf("want entity ID %s, got %s", typeid.FromUUID("testentity", tt.haveUUID), gotAggregate.ID())
			}

			// aggregate has initial version 0
			if gotAggregate.Version() != 0 {
				t.Errorf("want entity version 0, got %d", gotAggregate.Version())
			}
		})
	}
}

func TestEventSourcedStore_LoadAggregate(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)
	for _, tt := range []struct {
		name              string
		haveEventStore    func() eventstore.Store
		haveEntityFactory func(id uuid.UUID) *mockEntity
		haveAggregateID   typeid.UUID
		haveOpts          aggregatestore.LoadOptions
		wantVersion       int64
		wantEntity        *mockEntity
		wantErr           error
	}{
		{
			name:            "loads an aggregate by its ID using default options",
			haveAggregateID: aggregateID,
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 3)
			},
			wantVersion: 3,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name:            "loads an aggregate to a specific version by its ID ",
			haveAggregateID: aggregateID,
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 3)
			},
			haveOpts:    aggregatestore.LoadOptions{ToVersion: 2},
			wantVersion: 2,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 2,
			},
		},
		{
			name:            "returns an error when the aggregate ID is nil",
			haveAggregateID: typeid.FromUUID("mockentity", uuid.Nil),
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 3)
			},
			wantErr: errors.New("creating new aggregate: aggregate ID is nil"),
		},
		{
			name:            "returns an error when the aggregate cannot be hydrated",
			haveAggregateID: aggregateID,
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 3)
			},
			wantErr: errors.New("hydrating aggregate: aggregate not found"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewEventSourcedStore(tt.haveEventStore(), tt.haveEntityFactory)
			if err != nil {
				t.Errorf("unexpected error creating store: %v", err)
			}

			gotAggregate, err := store.Load(context.Background(), tt.haveAggregateID, tt.haveOpts)

			if tt.wantErr != nil {
				if err == nil || err.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			} else if gotAggregate == nil {
				t.Errorf("unexpected nil aggregate")
			}

			// aggregate has the correct ID
			if gotAggregate.ID().String() != typeid.FromUUID("mockentity", tt.haveAggregateID.UUID()).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.FromUUID("mockentity", tt.haveAggregateID.UUID()), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantVersion {
				t.Errorf("want aggregate version %d, got %d", tt.wantVersion, gotAggregate.Version())
			}
			// aggregate has the correct entity
			gotEntity := gotAggregate.Entity()
			if gotEntity == nil {
				t.Errorf("unexpected nil entity")
				return
			}
			// entity has the correct ID
			if gotEntity.ID.String() != tt.haveAggregateID.String() {
				t.Errorf("want entity ID %s, got %s", tt.haveAggregateID, gotEntity.ID)
			}
			// entity has the correct number of events (version) applied
			if gotEntity.numAppliedEvents != tt.wantVersion {
				t.Errorf("want applied events %v, got %v", tt.wantVersion, gotEntity.numAppliedEvents)
			}
		})
	}
}

func TestEventSourcedStore_HydrateAggregate(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)
	for _, tt := range []struct {
		name              string
		haveEventStore    func() eventstore.Store
		haveEntityFactory func(id uuid.UUID) *mockEntity
		haveStoreOpts     []aggregatestore.EventSourcedStoreOption[*mockEntity]
		haveAggregate     func() *aggregatestore.Aggregate[*mockEntity]
		haveOpts          aggregatestore.HydrateOptions
		wantVersion       int64
		wantEntity        *mockEntity
		wantErr           error
	}{
		{
			name: "hydrates an aggregate from version 0 to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{},
			wantVersion: 5,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 5,
			},
		},
		{
			name: "hydrates an aggregate from version 0 to a specific version",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 3},
			wantVersion: 3,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "hydrates an aggregate from version 0 to version 1",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 1},
			wantVersion: 1,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "hydrates an aggregate from version 0 to version N-1",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion: 4,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 4,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 1)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{},
			wantVersion: 5,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 4,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to a specific version",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 1)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 3},
			wantVersion: 3,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 2,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to version 2",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 1)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 2},
			wantVersion: 2,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to version N-1",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 1)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion: 4,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "hydrates an aggregate from a specific version to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 3)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{},
			wantVersion: 5,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 2,
			},
		},
		{
			name: "hydrates an aggregate from a specific version to another specific version",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 2)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion: 4,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 2,
			},
		},
		{
			name: "hydrates an aggregate from a specific version to version N-1",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 3)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion: 4,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "hydrates an aggregate from version N-1 to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 4)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{},
			wantVersion: 5,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "is a no-op when the aggregate is already at the target version",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 5)
				return agg
			},
			haveOpts:    aggregatestore.HydrateOptions{ToVersion: 5},
			wantVersion: 5,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 0,
			},
		},
		{
			name: "returns an error when the event stream reader is nil",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamReader[*mockEntity](nil),
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			haveOpts: aggregatestore.HydrateOptions{},
			wantErr:  errors.New("event store has no event stream reader"),
		},
		{
			name: "returns an error when the aggregate is nil",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				return nil
			},
			haveOpts: aggregatestore.HydrateOptions{},
			wantErr:  aggregatestore.ErrNilAggregate,
		},
		{
			name: "returns an error when the target version is invalid",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 5)
				return agg
			},
			haveOpts: aggregatestore.HydrateOptions{ToVersion: -1},
			wantErr:  errors.New("invalid target version"),
		},
		{
			name: "returns an error when the aggregate is at a more recent version than the target version",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 5)
				return agg
			},
			haveOpts: aggregatestore.HydrateOptions{ToVersion: 3},
			wantErr:  errors.New("aggregate is at more recent version (5) than requested version (3)"),
		},
		{
			name: "returns an error when the event stream is not found",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			wantErr: errors.New("aggregate not found"),
		},
		{
			name: "returns an error when unable to obtain an event stream iterator",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamReader[*mockEntity](mockStreamReader{
					readStreamErr: errors.New("mock error"),
				}),
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			haveOpts: aggregatestore.HydrateOptions{},
			wantErr:  errors.New("reading event stream: mock error"),
		},
		{
			name: "returns an error when unable to read an event from the event stream",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamReader[*mockEntity](mockStreamReader{
					readStreamIterator: mockStreamIterator{
						nextErr: errors.New("mock error"),
					},
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			wantErr: errors.New("reading event: mock error"),
		},
		{
			name: "returns an error when encountering an unregistered event type",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 3)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 3), 0)
				return agg
			},
			wantErr: errors.New("obtaining entity prototype: no prototype registered for event type mockEntityEventD"),
		},
		{
			name: "returns an error when unable to unmarshal an event store event",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventMarshaler[*mockEntity](mockEventMarshaler{
					unmarshalErr: errors.New("mock error"),
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			wantErr: errors.New("unmarshaling event data: mock error"),
		},
		{
			name: "returns an error when encountering an unexpected end of unapplied events",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				entity := newMockEntity(typeid.FromUUID("mockentity", id), 5)
				entity.applyEventErr = aggregatestore.ErrNoUnappliedEvents
				return entity
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(func() *mockEntity {
					entity := newMockEntity(aggregateID, 5)
					entity.applyEventErr = aggregatestore.ErrNoUnappliedEvents
					return entity
				}(), 0)
				return agg
			},
			wantErr: errors.New("applying aggregate event: unexpected end of unapplied events while hydrating aggregate"),
		},
		{
			name: "returns an error when unable to apply an event to the aggregate",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID), Data: mockEntityEventD{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID), Data: mockEntityEventE{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				entity := newMockEntity(typeid.FromUUID("mockentity", id), 5)
				entity.applyEventErr = errors.New("mock error")
				return entity
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(func() *mockEntity {
					entity := newMockEntity(aggregateID, 5)
					entity.applyEventErr = errors.New("mock error")
					return entity
				}(), 0)
				return agg
			},
			wantErr: errors.New("applying aggregate event: applying event: mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewEventSourcedStore(tt.haveEventStore(), tt.haveEntityFactory, tt.haveStoreOpts...)
			if err != nil {
				t.Errorf("unexpected error creating store: %v", err)
			}

			aggregate := tt.haveAggregate()
			hadID := typeid.FromUUID("mockentity", uuid.Nil)
			if aggregate != nil {
				hadID = aggregate.ID()
			}

			gotErr := store.Hydrate(context.Background(), aggregate, tt.haveOpts)

			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			// aggregate has the correct ID
			if aggregate.ID().String() != hadID.String() {
				t.Errorf("want aggregate ID %s, got %s", hadID.String(), aggregate.ID().String())
			}
			// aggregate has the correct version
			if aggregate.Version() != tt.wantVersion {
				t.Errorf("want aggregate version %d, got %d", tt.wantVersion, aggregate.Version())
			}
			// aggregate has a valid entity
			gotEntity := aggregate.Entity()
			if gotEntity == nil {
				t.Errorf("unexpected nil entity")
				return
			}
			// entity has the correct ID
			if gotEntity.ID.String() != tt.wantEntity.ID.String() {
				t.Errorf("want entity ID %s, got %s", tt.wantEntity.ID.String(), gotEntity.ID.String())
			}
			// entity has the expected number of events applied to it
			if gotEntity.numAppliedEvents != tt.wantEntity.numAppliedEvents {
				t.Errorf("want applied events %v, got %v", tt.wantEntity.numAppliedEvents, gotEntity.numAppliedEvents)
			}
		})
	}
}

func TestEventSourcedStore_SaveAggregate(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.Must(typeid.NewUUID("mockentity")).(typeid.UUID)
	for _, tt := range []struct {
		name              string
		haveEventStore    func() eventstore.Store
		haveEntityFactory func(id uuid.UUID) *mockEntity
		haveStoreOpts     []aggregatestore.EventSourcedStoreOption[*mockEntity]
		haveAggregate     func() *aggregatestore.Aggregate[*mockEntity]
		haveOpts          aggregatestore.SaveOptions
		wantVersion       int64
		wantEntity        *mockEntity
		wantErr           error
	}{
		{
			name: "saves a new aggregate with a single event using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 1)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 1), 0)
				agg.Append(&aggregatestore.AggregateEvent{
					ID:          typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID),
					Version:     1,
					Timestamp:   time.Now(),
					EntityEvent: mockEntityEventA{},
				})
				return agg
			},
			wantVersion: 1,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "saves a new aggregate with multiple events using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 1)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 3), 0)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID),
						Version:     1,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventA{},
					},
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID),
						Version:     2,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventB{},
					},
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID),
						Version:     3,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventC{},
					},
				)
				return agg
			},
			wantVersion: 3,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "saves an existing aggregate with a single new event using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 3)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID),
						Version:     4,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventD{},
					},
				)
				return agg
			},
			wantVersion: 4,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "saves an existing aggregate with multiple new events using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 3)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID),
						Version:     4,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventD{},
					},
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID),
						Version:     5,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventD{},
					},
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID),
						Version:     6,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventE{},
					},
				)
				return agg
			},
			wantVersion: 6,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "is a no-op when saving an aggregate with no unsaved events",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 3)
				return agg
			},
			wantVersion: 3,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 0,
			},
		},
		{
			name: "skips applying events to the aggregate after saving when the SkipApply option is true",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID), Data: mockEntityEventA{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventB")).(typeid.UUID), Data: mockEntityEventB{}.marshaledData()},
					{ID: typeid.Must(typeid.NewUUID("mockEntityEventC")).(typeid.UUID), Data: mockEntityEventC{}.marshaledData()},
				}, eventstore.AppendStreamOptions{})
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 3)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID),
						Version:     4,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventD{},
					},
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventD")).(typeid.UUID),
						Version:     5,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventD{},
					},
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventE")).(typeid.UUID),
						Version:     6,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventE{},
					},
				)
				return agg
			},
			haveOpts:    aggregatestore.SaveOptions{SkipApply: true},
			wantVersion: 3,
			wantEntity: &mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 0,
			},
		},
		{
			name: "returns an error when the aggregate is nil",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				return nil
			},
			wantErr: aggregatestore.ErrNilAggregate,
		},
		{
			name: "returns an error when the event stream writer is nil",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamWriter[*mockEntity](nil),
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			wantErr: errors.New("event store has no event stream writer"),
		},
		{
			name: "returns an error when a new aggregate at version 0 has no events to save",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				return agg
			},
			wantErr: errors.New("new aggregate has no events to save"),
		},
		{
			name: "returns an error when an unsaved event has a different version than the next expected version",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 3)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID),
						Version:     5,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventA{},
					},
				)
				return agg
			},
			wantErr: errors.New("event version mismatch: next is 4, event specifies 5"),
		},
		{
			name: "returns an error when unable to marshal an event store event",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventMarshaler[*mockEntity](mockEventMarshaler{
					marshalErr: errors.New("mock error"),
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID),
						Version:     1,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventA{},
					},
				)
				return agg
			},
			wantErr: errors.New("marshaling event data: mock error"),
		},
		{
			name: "returns an error when unable to append to the event stream",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				return newMockEntity(typeid.FromUUID("mockentity", id), 5)
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[*mockEntity]{
				aggregatestore.WithEventStreamWriter[*mockEntity](mockStreamWriter{
					appendStreamErr: errors.New("mock error"),
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(newMockEntity(aggregateID, 5), 0)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID),
						Version:     1,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventA{},
					},
				)
				return agg
			},
			wantErr: errors.New("saving events to stream: mock error"),
		},
		{
			name: "returns an errow when unable to apply an event to the aggregate",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveEntityFactory: func(id uuid.UUID) *mockEntity {
				entity := newMockEntity(typeid.FromUUID("mockentity", id), 5)
				entity.applyEventErr = errors.New("mock error")
				return entity
			},
			haveAggregate: func() *aggregatestore.Aggregate[*mockEntity] {
				agg := &aggregatestore.Aggregate[*mockEntity]{}
				agg.State().SetEntityAtVersion(func() *mockEntity {
					entity := newMockEntity(aggregateID, 5)
					entity.applyEventErr = errors.New("mock error")
					return entity
				}(), 0)
				agg.Append(
					&aggregatestore.AggregateEvent{
						ID:          typeid.Must(typeid.NewUUID("mockEntityEventA")).(typeid.UUID),
						Version:     1,
						Timestamp:   time.Now(),
						EntityEvent: mockEntityEventA{},
					},
				)
				return agg
			},
			wantErr: errors.New("applying aggregate event: applying event: mock error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.NewEventSourcedStore(tt.haveEventStore(), tt.haveEntityFactory, tt.haveStoreOpts...)
			if err != nil {
				t.Errorf("unexpected error creating store: %v", err)
			}

			aggregate := tt.haveAggregate()
			hadID := typeid.FromUUID("mockentity", uuid.Nil)
			if aggregate != nil {
				hadID = aggregate.ID()
			}

			gotErr := store.Save(context.Background(), aggregate, tt.haveOpts)

			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			// aggregate has the correct ID
			if aggregate.ID().String() != hadID.String() {
				t.Errorf("want aggregate ID %s, got %s", hadID.String(), aggregate.ID().String())
			}
			// aggregate has the correct version
			if aggregate.Version() != tt.wantVersion {
				t.Errorf("want aggregate version %d, got %d", tt.wantVersion, aggregate.Version())
			}
			// aggregate has a valid entity
			gotEntity := aggregate.Entity()
			if gotEntity == nil {
				t.Errorf("unexpected nil entity")
				return
			}
			// entity has the correct ID
			if gotEntity.ID.String() != tt.wantEntity.ID.String() {
				t.Errorf("want entity ID %s, got %s", tt.wantEntity.ID.String(), gotEntity.ID.String())
			}
			// entity has the expected number of events applied to it
			if gotEntity.numAppliedEvents != tt.wantEntity.numAppliedEvents {
				t.Errorf("want applied events %v, got %v", tt.wantEntity.numAppliedEvents, gotEntity.numAppliedEvents)
			}
		})
	}
}

func newMockEntity(id typeid.UUID, numEventTypes int) *mockEntity {
	eventTypes := []estoria.EntityEvent{
		mockEntityEventA{},
		mockEntityEventB{},
		mockEntityEventC{},
		mockEntityEventD{},
		mockEntityEventE{},
	}

	entity := &mockEntity{
		ID:         id,
		eventTypes: []estoria.EntityEvent{},
	}

	for i := range numEventTypes {
		entity.eventTypes = append(entity.eventTypes, eventTypes[i])
	}

	return entity
}
