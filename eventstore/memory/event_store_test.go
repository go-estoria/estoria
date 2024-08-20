package memory_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/typeid"
)

func TestEventStore_NewEventStore(t *testing.T) {
	for _, tt := range []struct {
		name    string
		opts    []memory.EventStoreOption
		wantErr error
	}{
		{
			name: "with default options, creates a new event store without error",
		},
		{
			name: "with a non-nil outbox, creates a new event store without error",
			opts: []memory.EventStoreOption{
				memory.WithOutbox(memory.NewOutbox()),
			},
		},
		{
			name: "with a non-nil custom marshaler, creates a new event store without error",
			opts: []memory.EventStoreOption{
				memory.WithEventMarshaler(failMarshaler{}),
			},
		},
		{
			name: "with a non-nil outbox and custom marshaler, creates a new event store without error",
			opts: []memory.EventStoreOption{
				memory.WithOutbox(memory.NewOutbox()),
				memory.WithEventMarshaler(failMarshaler{}),
			},
		},
		{
			name:    "returns an error if a nil outbox is provided",
			opts:    []memory.EventStoreOption{memory.WithOutbox(nil)},
			wantErr: errors.New("applying option: outbox cannot be nil"),
		},
		{
			name:    "returns an error if a nil marshaler is provided",
			opts:    []memory.EventStoreOption{memory.WithEventMarshaler(nil)},
			wantErr: errors.New("applying option: marshaler cannot be nil"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotStore, gotErr := memory.NewEventStore(tt.opts...)
			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("unexpected NewEventStore() error: wanted %v got %v", tt.wantErr, gotErr)
				}

				return
			}

			if gotStore == nil {
				t.Fatalf("unexpected nil event store")
			}
		})
	}
}

func TestEventStore_AppendStream(t *testing.T) {
	streamID := typeid.Must(typeid.NewUUID("streamtype")).(typeid.UUID)
	eventID := typeid.Must(typeid.NewUUID("eventtype")).(typeid.UUID)

	for _, tt := range []struct {
		name               string
		haveEventStoreOpts []memory.EventStoreOption
		haveStreamID       typeid.UUID
		haveAppendEvents   []eventSetWithOpts
	}{
		{
			name:         "with default options, appends individual events to a stream without error",
			haveStreamID: streamID,
			haveAppendEvents: []eventSetWithOpts{
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 1 data"),
						},
					},
				},
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 2 data"),
						},
					},
				},
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event3")).(typeid.UUID),
							Data: []byte("event 3 data"),
						},
					},
				},
			},
		},
		{
			name:         "with default options, appends batches of events to a stream without error",
			haveStreamID: streamID,
			haveAppendEvents: []eventSetWithOpts{
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 1 data"),
						},
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 2 data"),
						},
					},
				},
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 3 data"),
						},
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 4 data"),
						},
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 5 data"),
						},
					},
				},
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 6 data"),
						},
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 7 data"),
						},
					},
				},
			},
		},
		{
			name: "when an outbox is provided, appends events to the outbox",
			haveEventStoreOpts: []memory.EventStoreOption{
				memory.WithOutbox(memory.NewOutbox()),
			},
			haveStreamID: streamID,
			haveAppendEvents: []eventSetWithOpts{
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 1 data"),
						},
					},
				},
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 2 data"),
						},
						{
							ID:   typeid.Must(typeid.NewUUID("event3")).(typeid.UUID),
							Data: []byte("event 3 data"),
						},
					},
				},
			},
		},
		{
			name:         "returns ErrStreamVersionMismatch when expected version is less than actual version",
			haveStreamID: streamID,
			haveAppendEvents: []eventSetWithOpts{
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 1 data"),
						},
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 2 data"),
						},
					},
				},
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   eventID,
							Data: []byte("event 3 data"),
						},
					},
					opts: eventstore.AppendStreamOptions{
						ExpectVersion: 1,
					},
					wantErr: memory.ErrStreamVersionMismatch{
						StreamID:        streamID,
						EventID:         eventID,
						ExpectedVersion: 1,
						ActualVersion:   2,
					},
				},
			},
		},
		{
			name:         "returns ErrStreamVersionMismatch when expected version is greater than actual version",
			haveStreamID: streamID,
			haveAppendEvents: []eventSetWithOpts{
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 1 data"),
						},
						{
							ID:   typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
							Data: []byte("event 2 data"),
						},
					},
				},
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   eventID,
							Data: []byte("event 3 data"),
						},
					},
					opts: eventstore.AppendStreamOptions{
						ExpectVersion: 3,
					},
					wantErr: memory.ErrStreamVersionMismatch{
						StreamID:        streamID,
						EventID:         eventID,
						ExpectedVersion: 3,
						ActualVersion:   2,
					},
				},
			},
		},
		{
			name: "returns an error if an event fails to marshal",
			haveAppendEvents: []eventSetWithOpts{
				{
					events: []*eventstore.WritableEvent{
						{
							ID:   typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
							Data: []byte("event 1 data"),
						},
					},
					wantErr: errors.New("marshaling event: fake marshal error"),
				},
			},
			haveEventStoreOpts: []memory.EventStoreOption{
				memory.WithEventMarshaler(failMarshaler{}),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, err := memory.NewEventStore(tt.haveEventStoreOpts...)
			if err != nil {
				t.Fatalf("NewEventStore() error: %v", err)
			}

			totalEvents := 0
			for i, eventSet := range tt.haveAppendEvents {
				totalEvents += len(eventSet.events)
				gotErr := store.AppendStream(context.Background(), tt.haveStreamID, eventSet.events, eventSet.opts)
				if eventSet.wantErr != nil {
					if gotErr == nil || gotErr.Error() != eventSet.wantErr.Error() {
						t.Errorf("unexpected AppendStream() error (set %d): wanted %v got %v", i, eventSet.wantErr, gotErr)
					}

					return
				}
			}

			iter, err := store.ReadStream(context.Background(), tt.haveStreamID, eventstore.ReadStreamOptions{})
			if err != nil {
				t.Fatalf("ReadStream() error: %v", err)
			}

			var events []*eventstore.Event
			for {
				event, err := iter.Next(context.Background())
				if err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("Next() error: %v", err)
				}

				events = append(events, event)
			}

			if len(events) != totalEvents {
				t.Errorf("unexpected number of events: wanted %d got %d", len(tt.haveAppendEvents), len(events))
			}

			for i, event := range events {
				// all events have the correct stream ID
				if event.StreamID.String() != tt.haveStreamID.String() {
					t.Errorf("unexpected stream ID: wanted %s got %s", tt.haveStreamID.String(), event.StreamID.String())
				}
				// all events have the correct stream version
				if v := event.StreamVersion; v != int64(i+1) {
					t.Errorf("unexpected event version: wanted %d got %d", v, event.StreamVersion)
				}
				// all events have a valid timestamp
				if event.Timestamp.IsZero() {
					t.Errorf("unexpected empty event timestamp")
				}
			}
		})
	}
}

func TestEventStore_ReadStream(t *testing.T) {
	for _, tt := range []struct {
		name string
	}{
		{
			name: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Skip("todo")
		})
	}
}

type eventSetWithOpts struct {
	events  []*eventstore.WritableEvent
	opts    eventstore.AppendStreamOptions
	wantErr error
}

type failMarshaler struct{}

func (failMarshaler) Marshal(event *eventstore.Event) ([]byte, error) {
	return nil, errors.New("fake marshal error")
}

func (failMarshaler) Unmarshal(data []byte, dest *eventstore.Event) error {
	return errors.New("fake unmarshal error")
}
