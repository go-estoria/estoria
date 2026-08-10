package aggregatestore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type eventSourcedStoreTestCase[E any] struct {
	name           string
	haveEventStore func() eventstore.Store
	haveOpts       []aggregatestore.EventSourcedStoreOption[E]
	wantErr        error
}

type mockEntityEventA struct {
	A string `json:"a"`
}

func (e mockEntityEventA) EventType() string {
	return "mockEntityEventA"
}

func (e mockEntityEventA) New() estoria.DomainEvent[mockEntity] {
	return &mockEntityEventA{}
}

func (e mockEntityEventA) ApplyTo(m mockEntity) mockEntity {
	m.numAppliedEvents++
	return m
}

type mockEntityEventB struct {
	B string `json:"b"`
}

func (e mockEntityEventB) EventType() string {
	return "mockEntityEventB"
}

func (e mockEntityEventB) New() estoria.DomainEvent[mockEntity] {
	return &mockEntityEventB{}
}

func (e mockEntityEventB) ApplyTo(m mockEntity) mockEntity {
	m.numAppliedEvents++
	return m
}

type mockEntityEventC struct {
	C string `json:"c"`
}

func (e mockEntityEventC) EventType() string {
	return "mockEntityEventC"
}

func (e mockEntityEventC) New() estoria.DomainEvent[mockEntity] {
	return &mockEntityEventC{}
}

func (e mockEntityEventC) ApplyTo(m mockEntity) mockEntity {
	m.numAppliedEvents++
	return m
}

type mockEntityEventD struct {
	D string `json:"d"`
}

func (e mockEntityEventD) EventType() string {
	return "mockEntityEventD"
}

func (e mockEntityEventD) New() estoria.DomainEvent[mockEntity] {
	return &mockEntityEventD{}
}

func (e mockEntityEventD) ApplyTo(m mockEntity) mockEntity {
	m.numAppliedEvents++
	return m
}

type mockEntityEventE struct {
	E string `json:"e"`
}

func (e mockEntityEventE) EventType() string {
	return "mockEntityEventE"
}

func (e mockEntityEventE) New() estoria.DomainEvent[mockEntity] {
	return &mockEntityEventE{}
}

func (e mockEntityEventE) ApplyTo(m mockEntity) mockEntity {
	m.numAppliedEvents++
	return m
}

// mockEntityValueEvent exercises the value-typed prototype path: New() returns
// a value (not a pointer), so the store's registration must wrap it so the
// JSON marshaler can unmarshal into an addressable instance.
type mockEntityValueEvent struct {
	Value string `json:"value"`
}

func (e mockEntityValueEvent) EventType() string {
	return "mockEntityValueEvent"
}

func (e mockEntityValueEvent) New() estoria.DomainEvent[mockEntity] {
	return mockEntityValueEvent{}
}

func (e mockEntityValueEvent) ApplyTo(m mockEntity) mockEntity {
	m.numAppliedEvents++
	m.lastValueEventValue = e.Value
	return m
}

// mockEntityNilNewEvent is a malformed prototype whose New() returns nil. It's
// used to verify that registering such a prototype does not panic and that
// hydration still produces the existing "prototype.New() returned nil" error
// instead of a reflect panic.
type mockEntityNilNewEvent struct{}

func (e mockEntityNilNewEvent) EventType() string {
	return "mockEntityNilNewEvent"
}

func (e mockEntityNilNewEvent) New() estoria.DomainEvent[mockEntity] {
	return nil
}

func (e mockEntityNilNewEvent) ApplyTo(m mockEntity) mockEntity {
	return m
}

// mockEntityValueEventWithDefault is a value-typed event whose New() seeds a
// default field value. It's used to verify that the value-prototype constructor
// path doesn't bypass user-supplied defaults.
type mockEntityValueEventWithDefault struct {
	Value   string `json:"value"`
	Default string `json:"default,omitempty"`
}

func (e mockEntityValueEventWithDefault) EventType() string {
	return "mockEntityValueEventWithDefault"
}

func (e mockEntityValueEventWithDefault) New() estoria.DomainEvent[mockEntity] {
	return mockEntityValueEventWithDefault{Default: "seeded"}
}

func (e mockEntityValueEventWithDefault) ApplyTo(m mockEntity) mockEntity {
	m.numAppliedEvents++
	m.lastValueEventValue = e.Default
	return m
}

type mockStreamReader struct {
	readStreamIterator eventstore.StreamIterator
	readStreamErr      error
}

func (m mockStreamReader) ReadStream(_ context.Context, _ typeid.ID, _ eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	return m.readStreamIterator, m.readStreamErr
}

type mockStreamWriter struct {
	appendStreamErr error
}

func (m mockStreamWriter) AppendStream(_ context.Context, _ typeid.ID, _ []*eventstore.WritableEvent, _ eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	return nil, m.appendStreamErr
}

type mockStreamIterator struct {
	nextEvent *eventstore.Event
	nextErr   error
	closeErr  error
}

var _ eventstore.StreamIterator = mockStreamIterator{}

func (m mockStreamIterator) Next(_ context.Context) (*eventstore.Event, error) {
	return m.nextEvent, m.nextErr
}

func (m mockStreamIterator) Close(_ context.Context) error {
	return m.closeErr
}

// sequencedStreamIterator yields a fixed sequence of events, then reports end-of-stream.
// Unlike mockStreamIterator, it terminates, so a test that expects an error mid-stream
// fails fast rather than hanging if the error is never produced.
type sequencedStreamIterator struct {
	events []*eventstore.Event
	cursor int
}

func (m *sequencedStreamIterator) Next(_ context.Context) (*eventstore.Event, error) {
	if m.cursor >= len(m.events) {
		return nil, eventstore.ErrEndOfEventStream
	}

	event := m.events[m.cursor]
	m.cursor++
	return event, nil
}

func (m *sequencedStreamIterator) Close(_ context.Context) error {
	return nil
}

type mockEventMarshaler[E any] struct {
	marshaledBytes []byte
	marshalErr     error
	unmarshalErr   error
}

func (m mockEventMarshaler[E]) MarshalDomainEvent(_ estoria.DomainEvent[E]) ([]byte, error) {
	return m.marshaledBytes, m.marshalErr
}

func (m mockEventMarshaler[E]) UnmarshalDomainEvent(_ []byte, _ estoria.DomainEvent[E]) error {
	return m.unmarshalErr
}

func (m mockEventMarshaler[E]) ContentType() string {
	return "application/x-mock"
}

func TestNewEventSourcedStore(t *testing.T) {
	t.Parallel()

	for _, tt := range []eventSourcedStoreTestCase[mockEntity]{
		{
			name: "creates a new event sourced store with default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
		},
		{
			name: "creates a new event sourced store with a custom stream reader",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](func() eventstore.Store {
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
			haveOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamWriter[mockEntity](func() eventstore.Store {
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
			haveOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](func() eventstore.Store {
					return nil
				}()),
				aggregatestore.WithEventStreamWriter[mockEntity](func() eventstore.Store {
					return nil
				}()),
			},
			wantErr: aggregatestore.InitializeError{Err: errors.New("no event stream reader or writer provided")},
		},
		{
			name: "returns an error when a duplicate entity event prototype is registered",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventA{},
				),
			},
			wantErr: fmt.Errorf("applying option: %w", aggregatestore.InitializeError{
				Operation: "registering domain event prototype",
				Err:       errors.New("duplicate event type mockEntityEventA"),
			}),
		},
		{
			name: "returns an error when applying an option fails",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				func(_ *aggregatestore.EventSourcedStore[mockEntity]) error {
					return errors.New("test error")
				},
			},
			wantErr: aggregatestore.InitializeError{
				Operation: "applying option",
				Err:       errors.New("test error"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStore, err := aggregatestore.New(tt.haveEventStore(), "mockentity", newMockEntity, tt.haveOpts...)

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

func TestEventSourcedStore_LoadAggregate(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.NewV4("mockentity")

	for _, tt := range []struct {
		name            string
		haveEventStore  func() eventstore.Store
		haveStoreOpts   []aggregatestore.EventSourcedStoreOption[mockEntity]
		haveAggregateID typeid.ID
		haveLoadOpts    *aggregatestore.LoadOptions
		wantVersion     int64
		wantEntity      mockEntity
		wantErr         error
	}{
		{
			name:            "loads an aggregate by its ID using default options",
			haveAggregateID: aggregateID,
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
				),
			},
			wantVersion: 3,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name:            "loads an aggregate to a specific version by its ID",
			haveAggregateID: aggregateID,
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
				),
			},
			haveLoadOpts: &aggregatestore.LoadOptions{ToVersion: 2},
			wantVersion:  2,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 2,
			},
		},
		{
			name:            "returns an error when the aggregate ID is nil",
			haveAggregateID: typeid.New("mockentity", uuid.Nil),
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			wantErr: errors.New("aggregate ID is nil"),
		},
		{
			name:            "returns an error when the aggregate cannot be hydrated",
			haveAggregateID: aggregateID,
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			wantErr: errors.New("hydrating aggregate: aggregate not found"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.New(tt.haveEventStore(), "mockentity", newMockEntity, tt.haveStoreOpts...)
			if err != nil {
				t.Errorf("unexpected error creating store: %v", err)
			}

			gotAggregate, err := store.Load(context.Background(), tt.haveAggregateID.UUID, tt.haveLoadOpts)

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
			if gotAggregate.ID().String() != typeid.New("mockentity", tt.haveAggregateID.UUID).String() {
				t.Errorf("want aggregate ID %s, got %s", typeid.New("mockentity", tt.haveAggregateID.UUID), gotAggregate.ID())
			}
			// aggregate has the correct version
			if gotAggregate.Version() != tt.wantVersion {
				t.Errorf("want aggregate version %d, got %d", tt.wantVersion, gotAggregate.Version())
			}
			// aggregate has the correct entity
			gotEntity := gotAggregate.State()
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

	aggregateID := newMockEntity(uuid.Must(uuid.NewV4())).EntityID()
	for _, tt := range []struct {
		name            string
		haveEventStore  func() eventstore.Store
		haveStoreOpts   []aggregatestore.EventSourcedStoreOption[mockEntity]
		haveAggregate   func() *aggregatestore.Aggregate[mockEntity]
		haveHydrateOpts *aggregatestore.HydrateOptions
		wantVersion     int64
		wantEntity      mockEntity
		wantErr         error
	}{
		{
			name: "hydrates an aggregate from version 0 to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			haveHydrateOpts: nil,
			wantVersion:     5,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 5,
			},
		},
		{
			name: "hydrates an aggregate from version 0 to a specific version",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 3},
			wantVersion:     3,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "hydrates an aggregate from version 0 to version 1",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 1},
			wantVersion:     1,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "hydrates an aggregate from version 0 to version N-1",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion:     4,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 4,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 1)
			},
			haveHydrateOpts: nil,
			wantVersion:     5,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 4,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to a specific version",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 1)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 3},
			wantVersion:     3,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 2,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to version 2",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 1)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 2},
			wantVersion:     2,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "hydrates an aggregate from version 1 to version N-1",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 1)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion:     4,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "hydrates an aggregate from a specific version to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 3)
			},
			haveHydrateOpts: nil,
			wantVersion:     5,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 2,
			},
		},
		{
			name: "hydrates an aggregate from a specific version to another specific version",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 3)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion:     4,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "hydrates an aggregate from a specific version to version N+1",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 3)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 4},
			wantVersion:     4,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "hydrates an aggregate from version N-1 to the latest version using default options",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 4)
			},
			haveHydrateOpts: nil,
			wantVersion:     5,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "is a no-op when the aggregate is already at the target version",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
					mockEntityEventD{},
					mockEntityEventE{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 5)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 5},
			wantVersion:     5,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 0,
			},
		},
		{
			name: "returns an error when the event stream reader is nil",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](nil),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			haveHydrateOpts: nil,
			wantErr:         errors.New("event store has no event stream reader"),
		},
		{
			name: "returns an error when the aggregate is nil",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return nil
			},
			haveHydrateOpts: nil,
			wantErr:         aggregatestore.ErrNilAggregate,
		},
		{
			name: "returns an error when the target version is invalid",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 5)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: -1},
			wantErr:         errors.New("invalid hydrate options: ToVersion cannot be negative"),
		},
		{
			name: "returns an error when the aggregate is at a more recent version than the target version",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 5)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 3},
			wantErr:         errors.New("aggregate is at more recent version (5) than requested version (3)"),
		},
		{
			name: "returns an error when the event stream is not found",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			wantErr: errors.New("aggregate not found"),
		},
		{
			// An aggregate that already has state exists, so a store reporting an empty
			// filtered read as a missing stream means "nothing newer", not "not found".
			name: "hydrates nothing when the event stream is not found for an aggregate that already has state",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](mockStreamReader{
					readStreamErr: eventstore.ErrStreamNotFound,
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 10)
			},
			wantVersion: 10,
			wantEntity: mockEntity{
				ID: aggregateID,
			},
		},
		{
			// Consistent with a store that returns an empty iterator instead: the target
			// version is simply not reached, which is not an error.
			name: "hydrates nothing when the event stream is not found for an aggregate that already has state and a target version is set",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](mockStreamReader{
					readStreamErr: eventstore.ErrStreamNotFound,
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 10)
			},
			haveHydrateOpts: &aggregatestore.HydrateOptions{ToVersion: 15},
			wantVersion:     10,
			wantEntity: mockEntity{
				ID: aggregateID,
			},
		},
		{
			name: "returns an error when unable to obtain an event stream iterator",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](mockStreamReader{
					readStreamErr: errors.New("mock error"),
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			haveHydrateOpts: nil,
			wantErr:         errors.New("reading event stream: mock error"),
		},
		{
			name: "returns an error when unable to read an event from the event stream",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
					mockEntityEventD{D: "d"},
					mockEntityEventE{E: "e"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](mockStreamReader{
					readStreamIterator: mockStreamIterator{
						nextErr: errors.New("mock error"),
					},
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			wantErr: errors.New("projecting event stream: reading event: mock error"),
		},
		{
			name: "returns an error when encountering an unregistered event type",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			wantErr: errors.New("projecting event stream: processing event: obtaining domain event prototype: no prototype registered for event type 'mockEntityEventA'"),
		},
		{
			name: "returns an error when unable to unmarshal an event store event",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithDomainEventCodec(
					mockEventMarshaler[mockEntity]{
						unmarshalErr: errors.New("mock error"),
					},
				),
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			wantErr: errors.New("projecting event stream: processing event: unmarshaling event data: failed to unmarshal event data for event type 'mockEntityEventA': mock error"),
		},
		{
			// ApplyTo is total, so the only way applying can fail during hydration is an
			// event whose stream version does not line up with the aggregate's next version.
			name: "returns an error when an event arrives out of version order",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamReader[mockEntity](mockStreamReader{
					readStreamIterator: &sequencedStreamIterator{
						events: []*eventstore.Event{
							{
								ID:            typeid.NewV4("mockEntityEventA"),
								StreamVersion: 5,
								Data:          mustJSONMarshal(mockEntityEventA{A: "a"}),
							},
						},
					},
				}),
				aggregatestore.WithEventTypes(
					mockEntityEventA{},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			wantErr: errors.New("projecting event stream: processing event: applying aggregate event: failed to apply event type 'mockEntityEventA': event version mismatch: expected 1, got 5"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.New(tt.haveEventStore(), "mockentity", newMockEntity, tt.haveStoreOpts...)
			if err != nil {
				t.Errorf("unexpected error creating store: %v", err)
			}

			aggregate := tt.haveAggregate()
			hadID := typeid.New("mockentity", uuid.Nil)
			if aggregate != nil {
				hadID = aggregate.ID()
			}

			gotErr := store.Hydrate(context.Background(), aggregate, tt.haveHydrateOpts)

			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if gotErr != nil {
				t.Fatalf("unexpected error: %v", gotErr)
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
			gotEntity := aggregate.State()
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

	aggregateID := typeid.NewV4("mockentity")

	for _, tt := range []struct {
		name           string
		haveEventStore func() eventstore.Store
		haveStoreOpts  []aggregatestore.EventSourcedStoreOption[mockEntity]
		haveAggregate  func() *aggregatestore.Aggregate[mockEntity]
		haveSaveOpts   *aggregatestore.SaveOptions
		wantVersion    int64
		wantEntity     mockEntity
		wantErr        error
	}{
		{
			name: "saves a new aggregate with a single event using default options",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 0)
				agg.Append(mockEntityEventA{})
				return agg
			},
			wantVersion: 1,
			wantEntity: mockEntity{
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
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 0)
				agg.Append(
					mockEntityEventA{},
					mockEntityEventB{},
					mockEntityEventC{},
				)
				return agg
			},
			wantVersion: 3,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "saves an existing aggregate with a single new event using default options",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 3)
				agg.Append(
					mockEntityEventD{},
				)
				return agg
			},
			wantVersion: 4,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 1,
			},
		},
		{
			name: "saves an existing aggregate with multiple new events using default options",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 3)
				agg.Append(
					mockEntityEventD{},
					mockEntityEventD{},
					mockEntityEventE{},
				)
				return agg
			},
			wantVersion: 6,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 3,
			},
		},
		{
			name: "is a no-op when saving an aggregate with no unsaved events",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 3)
			},
			wantVersion: 3,
			wantEntity: mockEntity{
				ID:               aggregateID,
				numAppliedEvents: 0,
			},
		},
		{
			name: "skips applying events to the aggregate after saving when the SkipApply option is true",
			haveEventStore: func() eventstore.Store {
				events := []*eventstore.WritableEvent{}
				for _, event := range []estoria.DomainEvent[mockEntity]{
					mockEntityEventA{A: "a"},
					mockEntityEventB{B: "b"},
					mockEntityEventC{C: "c"},
				} {
					events = append(events, &eventstore.WritableEvent{
						Type: event.EventType(),
						Data: mustJSONMarshal(event),
					})
				}
				store, _ := memory.NewEventStore()
				store.AppendStream(context.Background(), aggregateID, events, eventstore.AppendStreamOptions{})
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 3)
				agg.Append(
					mockEntityEventD{},
					mockEntityEventD{},
					mockEntityEventE{},
				)
				return agg
			},
			haveSaveOpts: &aggregatestore.SaveOptions{SkipApply: true},
			wantVersion:  3,
			wantEntity: mockEntity{
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
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
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
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamWriter[mockEntity](nil),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			wantErr: errors.New("event store has no event stream writer"),
		},
		{
			name: "returns an error when a new aggregate at version 0 has no events to save",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				return newMockAggregate(aggregateID.UUID, 0)
			},
			wantErr: errors.New("new aggregate has no events to save"),
		},
		{
			name: "returns an error when unable to marshal an event store event",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithDomainEventCodec(
					mockEventMarshaler[mockEntity]{
						marshalErr: errors.New("mock error"),
					},
				),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 0)
				agg.Append(
					mockEntityEventA{},
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
			haveStoreOpts: []aggregatestore.EventSourcedStoreOption[mockEntity]{
				aggregatestore.WithEventStreamWriter[mockEntity](mockStreamWriter{
					appendStreamErr: errors.New("mock error"),
				}),
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 0)
				agg.Append(
					mockEntityEventA{},
				)
				return agg
			},
			wantErr: errors.New("saving events to stream: mock error"),
		},
		{
			// ApplyTo is total, so the post-append apply loop can only fail when the apply
			// queue disagrees with the version arithmetic — here forced by pre-seeding the
			// queue with an event whose version the aggregate cannot be at.
			name: "returns an error when a queued event is out of version order",
			haveEventStore: func() eventstore.Store {
				store, _ := memory.NewEventStore()
				return store
			},
			haveAggregate: func() *aggregatestore.Aggregate[mockEntity] {
				agg := newMockAggregate(aggregateID.UUID, 0)
				agg.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
					Version:     99,
					DomainEvent: mockEntityEventA{},
				})
				agg.Append(
					mockEntityEventA{A: "a"},
				)
				return agg
			},
			wantErr: errors.New("applying aggregate event: events appended but not applied to the aggregate: event version mismatch: expected 1, got 99"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := aggregatestore.New(tt.haveEventStore(), "mockentity", newMockEntity, tt.haveStoreOpts...)
			if err != nil {
				t.Errorf("unexpected error creating store: %v", err)
			}

			aggregate := tt.haveAggregate()
			hadID := typeid.New("mockentity", uuid.Nil)
			if aggregate != nil {
				hadID = aggregate.ID()
			}

			gotErr := store.Save(context.Background(), aggregate, tt.haveSaveOpts)

			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("want error: %v, got: %v", tt.wantErr, gotErr)
				}
				return
			}

			if gotErr != nil {
				t.Fatalf("unexpected error: %v", gotErr)
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
			gotEntity := aggregate.State()
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

// TestEventSourcedStore_HydratesValueTypedEvent verifies that an event type
// whose New() returns a value (not a pointer) can be registered and hydrated.
// Prior to the pointerConstructor wrapping in Use(), json.Unmarshal would
// reject the non-pointer destination and hydration would fail.
func TestEventSourcedStore_HydratesValueTypedEvent(t *testing.T) {
	t.Parallel()

	aggregateID := newMockEntity(uuid.Must(uuid.NewV4())).EntityID()

	es, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("unexpected error creating event store: %v", err)
	}
	if _, err := es.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{{
		Type: mockEntityValueEvent{}.EventType(),
		Data: mustJSONMarshal(mockEntityValueEvent{Value: "hello"}),
	}}, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("unexpected error appending event: %v", err)
	}

	store, err := aggregatestore.New(es, "mockentity", newMockEntity,
		aggregatestore.WithEventTypes(mockEntityValueEvent{}),
	)
	if err != nil {
		t.Fatalf("unexpected error creating store: %v", err)
	}

	aggregate, err := store.Load(context.Background(), aggregateID.UUID, nil)
	if err != nil {
		t.Fatalf("unexpected error loading aggregate: %v", err)
	}

	if got, want := aggregate.Version(), int64(1); got != want {
		t.Errorf("want version %d, got %d", want, got)
	}
	if got, want := aggregate.State().numAppliedEvents, int64(1); got != want {
		t.Errorf("want numAppliedEvents %d, got %d", want, got)
	}
	if got, want := aggregate.State().lastValueEventValue, "hello"; got != want {
		t.Errorf("want lastValueEventValue %q, got %q", want, got)
	}
}

// TestEventSourcedStore_PreservesValueTypedEventDefaults verifies that when a
// value-typed prototype's New() seeds default field values, those defaults
// survive into the unmarshaled event. The persisted payload below intentionally
// omits the "default" field; if pointerConstructor were calling reflect.New(t)
// without going through newFn(), the default would be lost.
func TestEventSourcedStore_PreservesValueTypedEventDefaults(t *testing.T) {
	t.Parallel()

	aggregateID := newMockEntity(uuid.Must(uuid.NewV4())).EntityID()

	es, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("unexpected error creating event store: %v", err)
	}
	// Persist a payload that omits the "default" field, so we know any value
	// that ends up on the entity came from the prototype's New(), not the JSON.
	if _, err := es.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{{
		Type: mockEntityValueEventWithDefault{}.EventType(),
		Data: []byte(`{"value":"hi"}`),
	}}, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("unexpected error appending event: %v", err)
	}

	store, err := aggregatestore.New(es, "mockentity", newMockEntity,
		aggregatestore.WithEventTypes(mockEntityValueEventWithDefault{}),
	)
	if err != nil {
		t.Fatalf("unexpected error creating store: %v", err)
	}

	aggregate, err := store.Load(context.Background(), aggregateID.UUID, nil)
	if err != nil {
		t.Fatalf("unexpected error loading aggregate: %v", err)
	}

	if got, want := aggregate.State().lastValueEventValue, "seeded"; got != want {
		t.Errorf("default from New() not preserved: want %q, got %q", want, got)
	}
}

// TestEventSourcedStore_NilReturningPrototypeIsHandledCleanly verifies that a
// prototype whose New() returns nil neither panics at registration nor produces
// a reflect panic on hydration; the existing nil check in the event handler
// surfaces a clean error.
func TestEventSourcedStore_NilReturningPrototypeIsHandledCleanly(t *testing.T) {
	t.Parallel()

	aggregateID := newMockEntity(uuid.Must(uuid.NewV4())).EntityID()

	es, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("unexpected error creating event store: %v", err)
	}
	if _, err := es.AppendStream(context.Background(), aggregateID, []*eventstore.WritableEvent{{
		Type: mockEntityNilNewEvent{}.EventType(),
		Data: []byte(`{}`),
	}}, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("unexpected error appending event: %v", err)
	}

	store, err := aggregatestore.New(es, "mockentity", newMockEntity,
		aggregatestore.WithEventTypes(mockEntityNilNewEvent{}),
	)
	if err != nil {
		t.Fatalf("unexpected error creating store: %v", err)
	}

	_, err = store.Load(context.Background(), aggregateID.UUID, nil)
	if err == nil {
		t.Fatal("expected hydration error for nil-returning prototype, got nil")
	}
	if !strings.Contains(err.Error(), "prototype.New() returned nil") {
		t.Errorf("want error containing 'prototype.New() returned nil', got: %v", err)
	}
}

func mustJSONMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
