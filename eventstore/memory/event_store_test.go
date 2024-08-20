package memory_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/typeid"
)

func TestEventStore_AppendStream(t *testing.T) {
	streamID := typeid.Must(typeid.NewUUID("streamtype")).(typeid.UUID)

	for _, tt := range []struct {
		name                 string
		haveEventStoreOpts   []memory.EventStoreOption
		haveStreamID         typeid.UUID
		haveAppendEvents     []*eventstore.Event
		haveAppendStreamOpts eventstore.AppendStreamOptions
		wantAppendErr        error
	}{
		{
			name:         "with default options, appends a single event to a new stream without error",
			haveStreamID: streamID,
			haveAppendEvents: []*eventstore.Event{
				{
					ID:            typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
					StreamID:      streamID,
					StreamVersion: 1,
					Timestamp:     time.Now(),
					Data:          []byte("event data"),
				},
			},
		},
		{
			name:         "with default options, appends multiple events to a new stream without error",
			haveStreamID: streamID,
			haveAppendEvents: []*eventstore.Event{
				{
					ID:            typeid.Must(typeid.NewUUID("event1")).(typeid.UUID),
					StreamID:      streamID,
					StreamVersion: 1,
					Timestamp:     time.Now(),
					Data:          []byte("event data"),
				},
				{
					ID:            typeid.Must(typeid.NewUUID("event2")).(typeid.UUID),
					StreamID:      streamID,
					StreamVersion: 2,
					Timestamp:     time.Now(),
					Data:          []byte("event data"),
				},
				{
					ID:            typeid.Must(typeid.NewUUID("event3")).(typeid.UUID),
					StreamID:      streamID,
					StreamVersion: 3,
					Timestamp:     time.Now(),
					Data:          []byte("event data"),
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := memory.NewEventStore(tt.haveEventStoreOpts...)
			gotErr := store.AppendStream(context.Background(), tt.haveStreamID, tt.haveAppendEvents, tt.haveAppendStreamOpts)
			if tt.wantAppendErr != nil {
				if gotErr == nil || gotErr.Error() != tt.wantAppendErr.Error() {
					t.Errorf("unexpected AppendStream() error: wanted %v got %v", tt.wantAppendErr, gotErr)
				}

				return
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

			if len(events) != len(tt.haveAppendEvents) {
				t.Errorf("unexpected number of events: wanted %d got %d", len(tt.haveAppendEvents), len(events))
			}

			for i, event := range events {
				if event.ID.String() != tt.haveAppendEvents[i].ID.String() {
					t.Errorf("unexpected event ID: wanted %s got %s", tt.haveAppendEvents[i].ID.String(), event.ID.String())
				}
				if event.StreamID.String() != tt.haveStreamID.String() {
					t.Errorf("unexpected stream ID: wanted %s got %s", tt.haveStreamID.String(), event.StreamID.String())
				}
				if event.StreamVersion != tt.haveAppendEvents[i].StreamVersion {
					t.Errorf("unexpected event version: wanted %d got %d", tt.haveAppendEvents[i].StreamVersion, event.StreamVersion)
				}
				if !event.Timestamp.Equal(tt.haveAppendEvents[i].Timestamp) {
					t.Errorf("unexpected event timestamp: wanted %v got %v", tt.haveAppendEvents[i].Timestamp, event.Timestamp)
				}
				if len(event.Data) != len(tt.haveAppendEvents[i].Data) {
					t.Errorf("unexpected event data length: wanted %v got %v", len(tt.haveAppendEvents[i].Data), len(event.Data))
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
