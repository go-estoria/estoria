package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	globalLog     []globalLogEntry
	mu            sync.RWMutex
	globalCounter int64
}

// A globalLogEntry is one committed event in the store's global order. The
// position rides alongside the envelope so a global read can seek without
// decoding, because positions can carry gaps: a failed append consumes counter
// values it never commits.
type globalLogEntry struct {
	position int64
	data     []byte
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

// AppendStream appends events to a stream and returns the written events.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (s *EventStore) AppendStream(_ context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate mutually exclusive options
	if opts.ExpectVersion != nil && opts.StreamMustNotExist {
		return nil, errors.New("ExpectVersion and StreamMustNotExist are mutually exclusive")
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
		return nil, eventstore.StreamVersionMismatchError{
			StreamID:        streamID,
			ExpectedVersion: 0,
			ActualVersion:   currentVersion,
		}
	}

	// Check ExpectVersion (nil means no check)
	if opts.ExpectVersion != nil && *opts.ExpectVersion != currentVersion {
		return nil, eventstore.StreamVersionMismatchError{
			StreamID:        streamID,
			ExpectedVersion: *opts.ExpectVersion,
			ActualVersion:   currentVersion,
		}
	}

	tx := []globalLogEntry{}
	written := make([]*eventstore.Event, 0, len(events))

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
			return nil, eventstore.EventMarshalingError{StreamID: streamID, EventID: event.ID, Err: err}
		}

		// Return the decoded form rather than the struct above, so the returned
		// event is byte-for-byte what a subsequent read yields after the store's
		// deliberate wire round trip — same normalized timestamp, same converged
		// nil-vs-empty maps.
		readBack := &eventstore.Event{}
		if err := json.Unmarshal(data, readBack); err != nil {
			return nil, eventstore.EventUnmarshalingError{StreamID: streamID, EventID: event.ID, Err: err}
		}

		tx = append(tx, globalLogEntry{position: globalPos, data: data})
		written = append(written, readBack)
	}

	for _, entry := range tx {
		s.events[streamID.String()] = append(s.events[streamID.String()], entry.data)
	}

	s.globalLog = append(s.globalLog, tx...)

	return written, nil
}

// ReadAll reads events from all streams in ascending global order.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (s *EventStore) ReadAll(_ context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// The log is position-ordered by construction, so the first event past the
	// bound is found by binary search; positions may carry gaps, so the bound
	// cannot be used as an index.
	start := sort.Search(len(s.globalLog), func(i int) bool {
		return s.globalLog[i].position > opts.AfterPosition
	})

	events := make([][]byte, 0, len(s.globalLog)-start)
	for _, entry := range s.globalLog[start:] {
		events = append(events, entry.data)
	}

	limit := int64(0)
	if opts.Count > 0 {
		limit = opts.Count
	}

	// An empty tail yields an iterator that immediately reports
	// ErrEndOfEventStream: a global read addresses no particular stream, so
	// there is no stream whose absence could be reported.
	return &streamIterator{
		events:    events,
		direction: eventstore.Forward,
		limit:     limit,
	}, nil
}

var _ eventstore.GlobalReader = (*EventStore)(nil)

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
