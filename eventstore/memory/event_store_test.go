package memory_test

import (
	"context"
	"errors"
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
			wantErr: eventstore.InitializationError{Err: errors.New("applying option: outbox cannot be nil")},
		},
		{
			name:    "returns an error if a nil marshaler is provided",
			opts:    []memory.EventStoreOption{memory.WithEventMarshaler(nil)},
			wantErr: eventstore.InitializationError{Err: errors.New("applying option: marshaler cannot be nil")},
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
	t.Parallel()
	streamIDs := []typeid.ID{
		typeid.NewV4("streamtype1"),
		typeid.NewV4("streamtype2"),
		typeid.NewV4("streamtype3"),
	}

	eventIDs := []typeid.ID{
		typeid.NewV4("eventtype1"),
		typeid.NewV4("eventtype2"),
		typeid.NewV4("eventtype3"),
		typeid.NewV4("eventtype4"),
		typeid.NewV4("eventtype5"),
	}

	for _, tt := range []struct {
		name               string
		haveEventStoreOpts []memory.EventStoreOption
		haveEvents         []eventsForStream
		haveStreamID       typeid.ID
		haveAppendEvents   []*eventstore.WritableEvent
		haveAppendOpts     eventstore.AppendStreamOptions
		wantEvents         []*eventstore.Event
		wantErr            error
	}{
		{
			name:         "appends an individual event to a new stream",
			haveStreamID: streamIDs[0],
			haveAppendEvents: []*eventstore.WritableEvent{
				{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
			},
			wantEvents: []*eventstore.Event{
				{ID: eventIDs[0], StreamID: streamIDs[0], StreamVersion: 1, Data: []byte("event 1 data")},
			},
		},
		{
			name:         "appends a batch of events to a new stream",
			haveStreamID: streamIDs[0],
			haveAppendEvents: []*eventstore.WritableEvent{
				{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
				{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
				{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[1],
					events: []*eventstore.WritableEvent{
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[2],
					events: []*eventstore.WritableEvent{
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[1],
			haveAppendEvents: []*eventstore.WritableEvent{
				{Type: eventIDs[3].Type, Data: []byte("event 4 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[1],
					events: []*eventstore.WritableEvent{
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
				{
					streamID: streamIDs[2],
					events: []*eventstore.WritableEvent{
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[1],
			haveAppendEvents: []*eventstore.WritableEvent{
				{Type: eventIDs[3].Type, Data: []byte("event 4 data")},
				{Type: eventIDs[4].Type, Data: []byte("event 5 data")},
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
			name:         "returns StreamVersionMismatchError when expected version is less than actual version",
			haveStreamID: streamIDs[0],
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
			},
			haveAppendEvents: []*eventstore.WritableEvent{
				{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
				{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
				{Type: eventIDs[3].Type, Data: []byte("event 4 data")},
			},
			haveAppendOpts: eventstore.AppendStreamOptions{
				ExpectVersion: 1,
			},
			wantErr: eventstore.StreamVersionMismatchError{
				StreamID:        streamIDs[0],
				ExpectedVersion: 1,
				ActualVersion:   3,
			},
		},
		{
			name: "returns StreamVersionMismatchError when expected version is greater than actual version",
			haveEvents: []eventsForStream{
				{
					streamID: streamIDs[0],
					events: []*eventstore.WritableEvent{
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
					},
				},
			},
			haveStreamID: streamIDs[0],
			haveAppendEvents: []*eventstore.WritableEvent{
				{Type: eventIDs[3].Type, Data: []byte("event 5 data")},
				{Type: eventIDs[4].Type, Data: []byte("event 6 data")},
			},
			haveAppendOpts: eventstore.AppendStreamOptions{
				ExpectVersion: 4,
			},
			wantErr: eventstore.StreamVersionMismatchError{
				StreamID:        streamIDs[0],
				ExpectedVersion: 4,
				ActualVersion:   3,
			},
		},
		{
			name: "returns an error if an event fails to marshal",
			haveEventStoreOpts: []memory.EventStoreOption{
				memory.WithEventMarshaler(failMarshaler{}),
			},
			haveStreamID: streamIDs[0],
			haveAppendEvents: []*eventstore.WritableEvent{
				{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
			},
			wantErr: eventstore.EventMarshalingError{
				StreamID: streamIDs[0],
				Err:      errors.New("fake marshal error"),
			},
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

				switch err := tt.wantErr.(type) {
				case eventstore.InitializationError:
					t.Logf("InitializationError: %v", err)
				case eventstore.StreamVersionMismatchError:
					t.Logf("StreamVersionMismatchError: %v", err)
					if err.StreamID.String() != tt.haveStreamID.String() {
						t.Errorf("unexpected stream ID: wanted %s got %s", tt.haveStreamID.String(), err.StreamID.String())
					}
					if err.ExpectedVersion != tt.haveAppendOpts.ExpectVersion {
						t.Errorf("unexpected expected version: wanted %d got %d", tt.haveAppendOpts.ExpectVersion, err.ExpectedVersion)
					}
					if err.ActualVersion != int64(totalEvents) {
						t.Errorf("unexpected actual version: wanted %d got %d", totalEvents, err.ActualVersion)
					}
				case eventstore.EventMarshalingError:
					t.Logf("EventMarshalingError: %v", err)
					if err.StreamID.String() != tt.haveStreamID.String() {
						t.Errorf("unexpected stream ID: wanted %s got %s", tt.haveStreamID.String(), err.StreamID.String())
					}
				case eventstore.EventUnmarshalingError:
					t.Logf("EventUnmarshalingError: %v", err)
					if err.StreamID.String() != tt.haveStreamID.String() {
						t.Errorf("unexpected stream ID: wanted %s got %s", tt.haveStreamID.String(), err.StreamID.String())
					}
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
				if errors.Is(err, eventstore.ErrEndOfEventStream) {
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
	t.Parallel()

	// predefined stream IDs used for matching in tests
	streamIDs := []typeid.ID{
		typeid.NewV4("streamtype1"),
		typeid.NewV4("streamtype2"),
		typeid.NewV4("streamtype3"),
	}

	// predefined event IDs used for matching in tests
	eventIDs := []typeid.ID{
		typeid.NewV4("eventtype1"),
		typeid.NewV4("eventtype2"),
		typeid.NewV4("eventtype3"),
		typeid.NewV4("eventtype4"),
		typeid.NewV4("eventtype5"),
	}

	for _, tt := range []struct {
		name               string
		haveEvents         []eventsForStream
		haveStreamID       typeid.ID
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
						{Type: eventIDs[3].Type, Data: []byte("event 4 data")},
						{Type: eventIDs[4].Type, Data: []byte("event 5 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
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
						{Type: eventIDs[0].Type, Data: []byte("event 1 data")},
						{Type: eventIDs[1].Type, Data: []byte("event 2 data")},
						{Type: eventIDs[2].Type, Data: []byte("event 3 data")},
						{Type: eventIDs[3].Type, Data: []byte("event 4 data")},
						{Type: eventIDs[4].Type, Data: []byte("event 5 data")},
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
			name:         "returns StreamNotFoundError for a non-existent stream",
			haveStreamID: streamIDs[0],
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
				if errors.Is(err, eventstore.ErrEndOfEventStream) {
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

func (failMarshaler) Marshal(_ *eventstore.Event) ([]byte, error) {
	return nil, errors.New("fake marshal error")
}

func (failMarshaler) Unmarshal(_ []byte, _ *eventstore.Event) error {
	return errors.New("fake unmarshal error")
}

type eventsForStream struct {
	streamID typeid.ID
	events   []*eventstore.WritableEvent
}
