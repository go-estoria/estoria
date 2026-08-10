package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

// EventStore is an in-memory event store. It should not be used in production applications.
//
// Events are stored JSON-encoded and decoded on every read. The round trip is deliberate:
// it normalizes values the way crossing a real backend's wire does — time.Time loses its
// monotonic reading, nil and empty maps converge, pointer identity breaks — so development
// against this store surfaces what would otherwise first break against a real one.
type EventStore struct {
	events        map[string][][]byte
	mu            sync.RWMutex
	globalCounter int64
}

// NewEventStore creates a new in-memory event store.
func NewEventStore(opts ...EventStoreOption) (*EventStore, error) {
	eventStore := &EventStore{
		events: map[string][][]byte{},
	}

	for _, opt := range opts {
		if err := opt(eventStore); err != nil {
			return nil, eventstore.InitializationError{Err: fmt.Errorf("applying option: %w", err)}
		}
	}

	return eventStore, nil
}

// AppendStream appends events to a stream.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (s *EventStore) AppendStream(_ context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate mutually exclusive options
	if opts.ExpectVersion != nil && opts.StreamMustNotExist {
		return errors.New("ExpectVersion and StreamMustNotExist are mutually exclusive")
	}

	stream, ok := s.events[streamID.String()]
	if !ok {
		// Note: the empty stream entry is intentionally left in the map on validation failure.
		// ReadStream handles this correctly by checking len(stream) == 0.
		s.events[streamID.String()] = [][]byte{}
		stream = s.events[streamID.String()]
	}

	currentVersion := int64(len(stream))

	// Check StreamMustNotExist
	if opts.StreamMustNotExist && currentVersion > 0 {
		return eventstore.StreamVersionMismatchError{
			StreamID:        streamID,
			ExpectedVersion: 0,
			ActualVersion:   currentVersion,
		}
	}

	// Check ExpectVersion (nil means no check)
	if opts.ExpectVersion != nil && *opts.ExpectVersion != currentVersion {
		return eventstore.StreamVersionMismatchError{
			StreamID:        streamID,
			ExpectedVersion: *opts.ExpectVersion,
			ActualVersion:   currentVersion,
		}
	}

	tx := [][]byte{}
	for i, writableEvent := range events {
		s.globalCounter++
		globalPos := s.globalCounter

		event := &eventstore.Event{
			ID:              typeid.NewV4(writableEvent.Type),
			StreamID:        streamID,
			StreamVersion:   int64(len(stream) + i + 1),
			GlobalPosition:  &globalPos,
			Timestamp:       time.Now(),
			Data:            writableEvent.Data,
			DataContentType: writableEvent.DataContentType,
			Metadata:        writableEvent.Metadata,
		}

		data, err := json.Marshal(event)
		if err != nil {
			return eventstore.EventMarshalingError{StreamID: streamID, EventID: event.ID, Err: err}
		}

		tx = append(tx, data)
	}

	s.events[streamID.String()] = append(stream, tx...)
	return nil
}

// startCursor returns the 0-based index into a stream of streamLen events at which a read
// begins, given the read's direction and version boundary.
func startCursor(streamLen int, opts eventstore.ReadStreamOptions) int64 {
	if opts.Direction != eventstore.Reverse {
		// Start at the event immediately after AfterVersion. AfterVersion is 1-based
		// version N, so the next event is at 0-based index N; 0 starts from the beginning.
		return opts.AfterVersion
	}

	// Read backwards starting at the event with AfterVersion (inclusive), converting the
	// 1-based version to a 0-based index; 0 starts from the end of the stream.
	if opts.AfterVersion <= 0 {
		return int64(streamLen - 1)
	}

	if cursor := opts.AfterVersion - 1; cursor < int64(streamLen) {
		return cursor
	}

	return int64(streamLen - 1)
}

// ReadStream reads events from a stream.
func (s *EventStore) ReadStream(_ context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stream, ok := s.events[streamID.String()]
	if !ok || len(stream) == 0 {
		return nil, eventstore.ErrStreamNotFound
	}

	cursor := startCursor(len(stream), opts)

	limit := int64(0)
	if opts.Count > 0 {
		limit = opts.Count
	}

	return &streamIterator{
		streamID:  streamID,
		events:    stream,
		cursor:    cursor,
		direction: opts.Direction,
		limit:     limit,
	}, nil
}

// An EventStoreOption configures an EventStore.
type EventStoreOption func(*EventStore) error
