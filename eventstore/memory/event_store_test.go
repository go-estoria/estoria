package memory_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/typeid"
)

func TestEventStore_NewEventStore(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		opts    []memory.EventStoreOption
		wantErr error
	}{
		{
			name: "creates a new event store",
		},
		{
			name: "with a non-nil outbox, creates a new event store",
			opts: []memory.EventStoreOption{
				memory.WithOutbox(memory.NewOutbox()),
			},
		},
		{
			name: "with a non-nil custom marshaler, creates a new event store",
			opts: []memory.EventStoreOption{
				memory.WithEventMarshaler(failMarshaler{}),
			},
		},
		{
			name: "with a non-nil outbox and custom marshaler, creates a new event store",
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
			t.Parallel()
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
	streamIDs := []typeid.UUID{
		typeid.Must(typeid.NewUUID("streamtype1")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("streamtype2")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("streamtype3")).(typeid.UUID),
	}

	eventIDs := []typeid.UUID{
		typeid.Must(typeid.NewUUID("eventtype1")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype2")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype3")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype4")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype5")).(typeid.UUID),
	}

	for _, tt := range []struct {
		name               string
		haveEventStoreOpts []memory.EventStoreOption
		haveEvents         []eventsForStream
		haveStreamID       typeid.UUID
		haveAppendEvents   []*eventstore.WritableEvent
		haveAppendOpts     eventstore.AppendStreamOptions
		wantEvents         []*eventstore.Event
		wantErr            error
	}{
		{
			name:         "appends an individual event to a new stream",
			haveStreamID: streamIDs[0],
			haveAppendEvents: []*eventstore.WritableEvent{
				{ID: eventIDs[0], Data: []byte("event 1 data")},
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[0], StreamID: streamIDs[0], StreamVersion: 1, Data: []byte("event 1 data")},
			},
		},
		{
			name:         "appends a batch of events to a new stream",
			haveStreamID: streamIDs[0],
			haveAppendEvents: []*eventstore.WritableEvent{
				{ID: eventIDs[0], Data: []byte("event 1 data")},
				{ID: eventIDs[1], Data: []byte("event 2 data")},
				{ID: eventIDs[2], Data: []byte("event 3 data")},
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[0], StreamID: streamIDs[0], StreamVersion: 1, Data: []byte("event 1 data")},
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
				{ID: eventIDs[2], StreamID: streamIDs[0], StreamVersion: 3, Data: []byte("event 3 data")},
			},
		},
		{
			name: "appends an individual event to an existing stream",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[1],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[2],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[1],
			haveAppendEvents: []*eventstore.WritableEvent{
				{ID: eventIDs[3], Data: []byte("event 4 data")},
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[0], StreamID: streamIDs[1], StreamVersion: 1, Data: []byte("event 1 data")},
				{ID: eventIDs[1], StreamID: streamIDs[1], StreamVersion: 2, Data: []byte("event 2 data")},
				{ID: eventIDs[2], StreamID: streamIDs[1], StreamVersion: 3, Data: []byte("event 3 data")},
				{ID: eventIDs[3], StreamID: streamIDs[1], StreamVersion: 4, Data: []byte("event 4 data")},
			},
		},
		{
			name: "appends a batch of events to an existing stream",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[1],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[2],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[1],
			haveAppendEvents: []*eventstore.WritableEvent{
				{ID: eventIDs[3], Data: []byte("event 4 data")},
				{ID: eventIDs[4], Data: []byte("event 5 data")},
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[0], StreamID: streamIDs[1], StreamVersion: 1, Data: []byte("event 1 data")},
				{ID: eventIDs[1], StreamID: streamIDs[1], StreamVersion: 2, Data: []byte("event 2 data")},
				{ID: eventIDs[2], StreamID: streamIDs[1], StreamVersion: 3, Data: []byte("event 3 data")},
				{ID: eventIDs[3], StreamID: streamIDs[1], StreamVersion: 4, Data: []byte("event 4 data")},
				{ID: eventIDs[4], StreamID: streamIDs[1], StreamVersion: 5, Data: []byte("event 5 data")},
			},
		},
		{
			name:         "returns ErrStreamVersionMismatch when expected version is less than actual version",
			haveStreamID: streamIDs[0],
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveAppendEvents: []*eventstore.WritableEvent{
				{ID: eventIDs[1], Data: []byte("event 2 data")},
				{ID: eventIDs[2], Data: []byte("event 3 data")},
				{ID: eventIDs[3], Data: []byte("event 4 data")},
			},
			haveAppendOpts: eventstore.AppendStreamOptions{
				ExpectVersion: 1,
			},
			wantErr: memory.ErrStreamVersionMismatch{
				StreamID:        streamIDs[0],
				EventID:         eventIDs[1],
				ExpectedVersion: 1,
				ActualVersion:   3,
			},
		},
		{
			name: "returns ErrStreamVersionMismatch when expected version is greater than actual version",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveAppendEvents: []*eventstore.WritableEvent{
				{ID: eventIDs[3], Data: []byte("event 5 data")},
				{ID: eventIDs[4], Data: []byte("event 6 data")},
			},
			haveAppendOpts: eventstore.AppendStreamOptions{
				ExpectVersion: 4,
			},
			wantErr: memory.ErrStreamVersionMismatch{
				StreamID:        streamIDs[0],
				EventID:         eventIDs[3],
				ExpectedVersion: 4,
				ActualVersion:   3,
			},
		},
		{
			name: "returns an error if an event fails to marshal",
			haveEventStoreOpts: []memory.EventStoreOption{
				memory.WithEventMarshaler(failMarshaler{}),
			},
			haveAppendEvents: []*eventstore.WritableEvent{
				{ID: eventIDs[0], Data: []byte("event 1 data")},
			},
			wantErr: errors.New("marshaling event: fake marshal error"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := memory.NewEventStore(tt.haveEventStoreOpts...)
			if err != nil {
				t.Fatalf("NewEventStore() error: %v", err)
			}

			totalEvents := 0
			if tt.haveEvents != nil {
				for _, eventSet := range tt.haveEvents {
					if err = store.AppendStream(context.Background(), eventSet.streamID, eventSet.events, eventstore.AppendStreamOptions{}); err != nil {
						t.Fatalf("AppendStream() error: %v", err)
					}

					totalEvents += len(eventSet.events)
				}
			}

			gotErr := store.AppendStream(context.Background(), tt.haveStreamID, tt.haveAppendEvents, tt.haveAppendOpts)
			if tt.wantErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantErr.Error() {
					t.Errorf("unexpected AppendStream() error: wanted %v got %v", tt.wantErr, gotErr)
				}

				return
			}

			totalEvents += len(tt.haveAppendEvents)

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

			if len(events) != len(tt.wantEvents) {
				t.Errorf("unexpected number of events: wanted %d got %d", len(tt.wantEvents), len(events))
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
	// predefined stream IDs used for matching in tests
	streamIDs := []typeid.UUID{
		typeid.Must(typeid.NewUUID("streamtype1")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("streamtype2")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("streamtype3")).(typeid.UUID),
	}

	// predefined event IDs used for matching in tests
	eventIDs := []typeid.UUID{
		typeid.Must(typeid.NewUUID("eventtype1")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype2")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype3")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype4")).(typeid.UUID),
		typeid.Must(typeid.NewUUID("eventtype5")).(typeid.UUID),
	}

	for _, tt := range []struct {
		name               string
		haveEvents         []eventsForStream
		haveStreamID       typeid.UUID
		haveReadStreamOpts eventstore.ReadStreamOptions
		wantEvents         []*eventstore.Event
		wantErr            error
	}{
		{
			name: "returns a stream iterator for an existing stream",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[0], StreamID: streamIDs[0], StreamVersion: 1, Data: []byte("event 1 data")},
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
				{ID: eventIDs[2], StreamID: streamIDs[0], StreamVersion: 3, Data: []byte("event 3 data")},
			},
		},
		{
			name: "returns a stream iterator for an existing stream with an initial offset",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveReadStreamOpts: eventstore.ReadStreamOptions{
				Offset: 1,
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
				{ID: eventIDs[2], StreamID: streamIDs[0], StreamVersion: 3, Data: []byte("event 3 data")},
			},
		},
		{
			name: "returns a stream iterator for an existing stream with a maximum count",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveReadStreamOpts: eventstore.ReadStreamOptions{
				Count: 2,
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[0], StreamID: streamIDs[0], StreamVersion: 1, Data: []byte("event 1 data")},
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
			},
		},
		{
			name: "returns a stream iterator for an existing stream with an initial offset and maximum count",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
						{ID: eventIDs[3], Data: []byte("event 4 data")},
						{ID: eventIDs[4], Data: []byte("event 5 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveReadStreamOpts: eventstore.ReadStreamOptions{
				Offset: 2,
				Count:  2,
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[2], StreamID: streamIDs[0], StreamVersion: 3, Data: []byte("event 3 data")},
				{ID: eventIDs[3], StreamID: streamIDs[0], StreamVersion: 4, Data: []byte("event 4 data")},
			},
		},
		{
			name: "returns a stream iterator for an existing stream in reverse order",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveReadStreamOpts: eventstore.ReadStreamOptions{
				Direction: eventstore.Reverse,
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[2], StreamID: streamIDs[0], StreamVersion: 3, Data: []byte("event 3 data")},
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
				{ID: eventIDs[0], StreamID: streamIDs[0], StreamVersion: 1, Data: []byte("event 1 data")},
			},
		},
		{
			name: "returns a stream iterator for an existing stream in reverse order with an initial offset",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveReadStreamOpts: eventstore.ReadStreamOptions{
				Direction: eventstore.Reverse,
				Offset:    1,
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
				{ID: eventIDs[0], StreamID: streamIDs[0], StreamVersion: 1, Data: []byte("event 1 data")},
			},
		},
		{
			name: "returns a stream iterator for an existing stream in reverse order with a maximum count",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveReadStreamOpts: eventstore.ReadStreamOptions{
				Direction: eventstore.Reverse,
				Count:     2,
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[2], StreamID: streamIDs[0], StreamVersion: 3, Data: []byte("event 3 data")},
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
			},
		},
		{
			name: "returns a stream iterator for an existing stream in reverse order with an initial offset and maximum count",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{ID: eventIDs[0], Data: []byte("event 1 data")},
						{ID: eventIDs[1], Data: []byte("event 2 data")},
						{ID: eventIDs[2], Data: []byte("event 3 data")},
						{ID: eventIDs[3], Data: []byte("event 4 data")},
						{ID: eventIDs[4], Data: []byte("event 5 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveReadStreamOpts: eventstore.ReadStreamOptions{
				Direction: eventstore.Reverse,
				Offset:    2,
				Count:     2,
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[2], StreamID: streamIDs[0], StreamVersion: 3, Data: []byte("event 3 data")},
				{ID: eventIDs[1], StreamID: streamIDs[0], StreamVersion: 2, Data: []byte("event 2 data")},
			},
		},
		{
			name:         "returns ErrStreamNotFound for a non-existent stream",
			haveStreamID: typeid.Must(typeid.NewUUID("streamtype")).(typeid.UUID),
			wantErr:      eventstore.ErrStreamNotFound,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := memory.NewEventStore()
			if err != nil {
				t.Fatalf("NewEventStore() error: %v", err)
			}

			for _, eventSet := range tt.haveEvents {
				if err = store.AppendStream(context.Background(), eventSet.streamID, eventSet.events, eventstore.AppendStreamOptions{}); err != nil {
					t.Fatalf("AppendStream() error: %v", err)
				}
			}

			iter, err := store.ReadStream(context.Background(), tt.haveStreamID, tt.haveReadStreamOpts)
			if tt.wantErr != nil {
				if err == nil || err.Error() != tt.wantErr.Error() {
					t.Errorf("unexpected ReadStream() error: wanted %v got %v", tt.wantErr, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("ReadStream() error: %v", err)
			}

			var gotEvents []*eventstore.Event
			for {
				gotEvent, err := iter.Next(context.Background())
				if err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("Next() error: %v", err)
				}

				gotEvents = append(gotEvents, gotEvent)
			}

			// expected number of events
			if len(gotEvents) != len(tt.wantEvents) {
				t.Fatalf("unexpected number of events: wanted %d got %d", len(tt.wantEvents), len(gotEvents))
			}

			for i, gotEvent := range gotEvents {
				// all events have the correct ID
				if gotEvent.ID.String() != tt.wantEvents[i].ID.String() {
					t.Errorf("unexpected event ID: wanted %s got %s", tt.wantEvents[i].ID.String(), gotEvent.ID.String())
				}

				// all events have the correct stream ID
				if gotEvent.StreamID.String() != tt.haveStreamID.String() {
					t.Errorf("unexpected stream ID: wanted %s got %s", tt.haveStreamID.String(), gotEvent.StreamID.String())
				}

				// all events have the correct stream version
				if gotEvent.StreamVersion != tt.wantEvents[i].StreamVersion {
					t.Errorf("unexpected event version: wanted %d got %d", tt.wantEvents[i].StreamVersion, gotEvent.StreamVersion)
				}

				// all events have a valid timestamp
				if gotEvent.Timestamp.IsZero() {
					t.Errorf("unexpected empty event timestamp")
				}

				// all events have the correct data length
				if len(gotEvent.Data) != len(tt.wantEvents[i].Data) {
					t.Errorf("unexpected event data: wanted %s got %s", tt.wantEvents[i].Data, gotEvent.Data)
				}

				// all events have the correct data
				if string(gotEvent.Data) != string(tt.wantEvents[i].Data) {
					t.Errorf("unexpected event data: wanted %s got %s", tt.wantEvents[i].Data, gotEvent.Data)
				}
			}
		})
	}
}

type failMarshaler struct{}

func (failMarshaler) Marshal(event *eventstore.Event) ([]byte, error) {
	return nil, errors.New("fake marshal error")
}

func (failMarshaler) Unmarshal(data []byte, dest *eventstore.Event) error {
	return errors.New("fake unmarshal error")
}

type eventsForStream struct {
	streamID typeid.UUID
	events   []*eventstore.WritableEvent
}

func randomEvents(count int) []*eventstore.WritableEvent {
	events := make([]*eventstore.WritableEvent, count)
	for i := range count {
		events[i] = &eventstore.WritableEvent{
			ID:   typeid.Must(typeid.NewUUID(fmt.Sprintf("eventtype%d", i))).(typeid.UUID),
			Data: []byte(fmt.Sprintf("event data %d", i)),
		}
	}

	return events
}
