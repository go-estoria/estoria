package aggregatestore_test

import (
	"context"
	"errors"
	"testing"

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

type mockEntity struct {
	ID         typeid.UUID
	eventTypes []estoria.EntityEvent

	EventTypeAApplied bool
	EventTypeBApplied bool
	EventTypeCApplied bool
}

var _ estoria.Entity = &mockEntity{}

func (e mockEntity) EntityID() typeid.UUID {
	return e.ID
}

func (e mockEntity) EventTypes() []estoria.EntityEvent {
	return e.eventTypes
}

func (e *mockEntity) ApplyEvent(_ context.Context, event estoria.EntityEvent) error {
	switch event.EventType() {
	case "mockEntityEventA":
		e.EventTypeAApplied = true
	case "mockEntityEventB":
		e.EventTypeBApplied = true
	case "mockEntityEventC":
		e.EventTypeCApplied = true
	default:
		return errors.New("unknown event type")
	}

	return nil
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

type mockMarshaler struct{}

func (m mockMarshaler) Marshal(_ *estoria.EntityEvent) ([]byte, error) {
	return nil, nil
}

func (m mockMarshaler) Unmarshal(_ []byte, _ *estoria.EntityEvent) error {
	return nil
}

func TestNewEventSourcedStore(t *testing.T) {
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
				aggregatestore.WithEntityEventMarshaler[*mockEntity](mockMarshaler{}),
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
				aggregatestore.WithStreamReader[*mockEntity](func() eventstore.Store {
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
				aggregatestore.WithStreamWriter[*mockEntity](func() eventstore.Store {
					store, _ := memory.NewEventStore()
					return store
				}()),
			},
		},
		// {
		// 	name: "returns an error when no event store is provided",
		// 	haveEntityFactory: func(id uuid.UUID) *mockEntity {
		// 		return &mockEntity{id: typeid.FromUUID("testentity", id)}
		// 	},
		// 	wantErr: aggregatestore.InitializeAggregateStoreError{
		// 		Err: errors.New("no event stream reader or writer provided"),
		// 	},
		// },
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
				func(store *aggregatestore.EventSourcedStore[*mockEntity]) error {
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
				return &mockEntity{
					ID: typeid.FromUUID("mockentity", id),
					eventTypes: []estoria.EntityEvent{
						mockEntityEventA{},
						mockEntityEventB{},
						mockEntityEventC{},
					},
				}
			},
			wantVersion: 3,
			wantEntity: &mockEntity{
				ID:                aggregateID,
				EventTypeAApplied: true,
				EventTypeBApplied: true,
				EventTypeCApplied: true,
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
				return &mockEntity{
					ID: typeid.FromUUID("mockentity", id),
					eventTypes: []estoria.EntityEvent{
						mockEntityEventA{},
						mockEntityEventB{},
						mockEntityEventC{},
					},
				}
			},
			haveOpts:    aggregatestore.LoadOptions{ToVersion: 2},
			wantVersion: 2,
			wantEntity: &mockEntity{
				ID:                aggregateID,
				EventTypeAApplied: true,
				EventTypeBApplied: true,
				EventTypeCApplied: false,
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
				return &mockEntity{
					ID: typeid.FromUUID("mockentity", id),
					eventTypes: []estoria.EntityEvent{
						mockEntityEventA{},
						mockEntityEventB{},
						mockEntityEventC{},
					},
				}
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
				return &mockEntity{
					ID: typeid.FromUUID("mockentity", id),
					eventTypes: []estoria.EntityEvent{
						mockEntityEventA{},
						mockEntityEventB{},
						mockEntityEventC{},
					},
				}
			},
			wantErr: errors.New("hydrating aggregate: reading event stream: stream not found: " + aggregateID.String()),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
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
			}
			if gotEntity.ID.String() != tt.haveAggregateID.String() {
				t.Errorf("want entity ID %s, got %s", tt.haveAggregateID, gotEntity.ID)
			}
			if gotEntity.EventTypeAApplied != tt.wantEntity.EventTypeAApplied {
				t.Errorf("want EventTypeAApplied %v, got %v", tt.wantEntity.EventTypeAApplied, gotEntity.EventTypeAApplied)
			}
			if gotEntity.EventTypeBApplied != tt.wantEntity.EventTypeBApplied {
				t.Errorf("want EventTypeBApplied %v, got %v", tt.wantEntity.EventTypeBApplied, gotEntity.EventTypeBApplied)
			}
			if gotEntity.EventTypeCApplied != tt.wantEntity.EventTypeCApplied {
				t.Errorf("want EventTypeCApplied %v, got %v", tt.wantEntity.EventTypeCApplied, gotEntity.EventTypeCApplied)
			}
		})
	}
}

func TestEventSourcedStore_HydrateAggregate(t *testing.T) {
	for _, tt := range []struct {
		name string
	}{
		{
			name: "hydrates an aggregate from version 0 to the latest version using default options",
		},
		{
			name: "hydrates an aggregate from version 0 to a specific version",
		},
		{
			name: "hydrates an aggregate from version 0 to version 1",
		},
		{
			name: "hydrates an aggregate from version 0 to version N-1",
		},
		{
			name: "hydrates an aggregate from version 1 to the latest version using default options",
		},
		{
			name: "hydrates an aggregate from version 1 to a specific version",
		},
		{
			name: "hydrates an aggregate from version 1 to version 2",
		},
		{
			name: "hydrates an aggregate from version 1 to version N-1",
		},
		{
			name: "hydrates an aggregate from a specific version to the latest version using default options",
		},
		{
			name: "hydrates an aggregate from a specific version to another specific version",
		},
		{
			name: "hydrates an aggregate from a specific version to version N-1",
		},
		{
			name: "hydrates an aggregate from version N-1 to the latest version using default options",
		},
		{
			name: "is a no-op when the aggregate is already at the target version",
		},
		{
			name: "returns an error when the event stream reader is nil",
		},
		{
			name: "returns an error when the aggregate is nil",
		},
		{
			name: "returns an error when the target version is invalid",
		},
		{
			name: "returns an error when the aggregate is at a more recent version than the target version",
		},
		{
			name: "returns an error when the event stream is not found",
		},
		{
			name: "returns an error when unable to obtain an event stream iterator",
		},
		{
			name: "returns an error when unable to read an event from the event stream",
		},
		{
			name: "returns an error when encountering an unregistered event type",
		},
		{
			name: "returns an error when unable to unmarshal an event store event",
		},
		{
			name: "returns an error when unable to apply an event to the aggregate",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {

		})
	}
}

func TestEventSourcedStore_SaveAggregate(t *testing.T) {
	for _, tt := range []struct {
		name string
	}{
		{},
	} {
		t.Run(tt.name, func(t *testing.T) {

		})
	}
}
