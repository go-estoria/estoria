package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	streams       map[string]*stream
	globalLog     []globalLogEntry
	mu            sync.RWMutex
	globalCounter int64
}

// A stream holds one stream's committed event envelopes. Versions are not
// derivable from slice indexes alone: truncation removes leading events while
// later events keep their versions, so the version of events[0] rides
// alongside. A never-written stream has no entry; an entry with no events and
// firstVersion 1 was created but never committed to; an entry with no events
// and a higher firstVersion was truncated empty and keeps its version counter.
type stream struct {
	firstVersion int64
	events       [][]byte
}

// tip returns the stream's latest committed version, or firstVersion-1 when
// the stream is empty.
func (s *stream) tip() int64 {
	return s.firstVersion + int64(len(s.events)) - 1
}

// A globalLogEntry is one committed event in the store's global order. The
// position rides alongside the envelope so a global read can seek without
// decoding, and the stream ID and version identify the entry to deletion.
// Position allocation and publication happen inside AppendStream's single
// critical section, so the log grows in position order and the stable-prefix
// promise holds by construction.
type globalLogEntry struct {
	position int64
	streamID string
	version  int64
	data     []byte
}

// NewEventStore creates a new in-memory event store.
func NewEventStore(opts ...EventStoreOption) (*EventStore, error) {
	eventStore := &EventStore{
		streams: map[string]*stream{},
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

	entry, ok := s.streams[streamID.String()]
	if !ok {
		// Note: the empty stream entry is intentionally left in the map on validation failure.
		// ReadStream handles this correctly by treating it as never written.
		entry = &stream{firstVersion: 1}
		s.streams[streamID.String()] = entry
	}

	currentVersion := entry.tip()

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
			StreamVersion:   currentVersion + int64(i) + 1,
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

		tx = append(tx, globalLogEntry{
			position: globalPos,
			streamID: streamID.String(),
			version:  event.StreamVersion,
			data:     data,
		})
		written = append(written, readBack)
	}

	for _, logEntry := range tx {
		entry.events = append(entry.events, logEntry.data)
	}

	s.globalLog = append(s.globalLog, tx...)

	return written, nil
}

// startCursor returns the 0-based index into a stream's events at which a read
// begins, given the read's direction and version boundary.
func startCursor(entry *stream, opts eventstore.ReadStreamOptions) int64 {
	if opts.Direction != eventstore.Reverse {
		// Start at the event immediately after AfterVersion; 0 starts from the
		// beginning. Truncation can place the whole stream above the boundary,
		// in which case the read starts at the first retained event.
		if cursor := opts.AfterVersion - entry.firstVersion + 1; cursor > 0 {
			return cursor
		}

		return 0
	}

	// Read backwards starting at the event with AfterVersion (inclusive);
	// 0 starts from the end of the stream.
	if opts.AfterVersion <= 0 {
		return int64(len(entry.events) - 1)
	}

	if cursor := opts.AfterVersion - entry.firstVersion; cursor < int64(len(entry.events)) {
		// A boundary below the first retained event yields a negative cursor,
		// which the iterator reports as an exhausted stream.
		return cursor
	}

	return int64(len(entry.events) - 1)
}

// ReadStream reads events from a stream.
func (s *EventStore) ReadStream(_ context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.streams[streamID.String()]
	if !ok || (len(entry.events) == 0 && entry.firstVersion == 1) {
		return nil, eventstore.ErrStreamNotFound
	}

	cursor := startCursor(entry, opts)

	limit := int64(0)
	if opts.Count > 0 {
		limit = opts.Count
	}

	return &streamIterator{
		streamID:  streamID,
		events:    entry.events,
		cursor:    cursor,
		direction: opts.Direction,
		limit:     limit,
	}, nil
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

// DeleteStream deletes events from a stream.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (s *EventStore) DeleteStream(_ context.Context, streamID typeid.ID, opts eventstore.DeleteStreamOptions) error {
	if opts.ToVersion < 0 {
		return errors.New("ToVersion must not be negative")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := streamID.String()

	entry, ok := s.streams[key]
	if !ok || (len(entry.events) == 0 && entry.firstVersion == 1) {
		return eventstore.ErrStreamNotFound
	}

	if opts.ToVersion == 0 {
		delete(s.streams, key)
		s.dropFromGlobalLog(key, math.MaxInt64)
		return nil
	}

	cut := min(opts.ToVersion, entry.tip())

	removed := cut - entry.firstVersion + 1
	if removed <= 0 {
		return nil
	}

	entry.events = entry.events[removed:]
	entry.firstVersion = cut + 1
	s.dropFromGlobalLog(key, cut)

	return nil
}

// dropFromGlobalLog removes a stream's entries at or below toVersion from the
// global log, preserving the positions of everything retained.
func (s *EventStore) dropFromGlobalLog(streamID string, toVersion int64) {
	kept := s.globalLog[:0]
	for _, entry := range s.globalLog {
		if entry.streamID == streamID && entry.version <= toVersion {
			continue
		}

		kept = append(kept, entry)
	}

	s.globalLog = kept
}

var (
	_ eventstore.GlobalReader  = (*EventStore)(nil)
	_ eventstore.StreamDeleter = (*EventStore)(nil)
)

// An EventStoreOption configures an EventStore.
type EventStoreOption func(*EventStore) error
