package eventstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An Store can read and write events to a stream.
type Store interface {
	StreamReader
	StreamWriter
}

// An StreamReader can read events from a stream.
type StreamReader interface {
	// ReadStream creates an event stream iterator for reading events from a stream.
	// The starting point, direction, and number of events to read can be specified in the options.
	//
	// Implementations should return ErrStreamNotFound only when the stream itself does not
	// exist. A stream that exists but has no events matching the options (for example a
	// forward read with an AfterVersion at or beyond the stream's latest version) should
	// yield an iterator that immediately reports ErrEndOfEventStream.
	//
	// The distinction is required rather than advisory, and eventstore/storetest enforces
	// it. Making it can cost an implementation an extra existence check; not making it
	// leaves any aggregate snapshotted at its own stream tip unloadable, because the read
	// for events after the snapshot reports the whole stream missing.
	ReadStream(ctx context.Context, id typeid.ID, opts ReadStreamOptions) (StreamIterator, error)
}

// An StreamIterator reads events from a stream.
type StreamIterator interface {
	// Next reads the next event from the stream.
	// It returns ErrEndOfEventStream when there are no more events.
	Next(ctx context.Context) (*Event, error)

	// Close closes the stream iterator.
	Close(ctx context.Context) error
}

// ReadStreamOptions are options for reading an event stream.
type ReadStreamOptions struct {
	// AfterVersion specifies the stream version boundary for reading.
	//
	// For forward reads (Direction == Forward):
	//   Events with StreamVersion > AfterVersion are returned (exclusive lower bound).
	//   AfterVersion=2 returns events at versions 3, 4, 5, ...
	//
	// For reverse reads (Direction == Reverse):
	//   Events with StreamVersion <= AfterVersion are returned (inclusive upper bound),
	//   reading backwards from AfterVersion toward version 1.
	//   AfterVersion=3 returns events at versions 3, 2, 1.
	//
	// Default: 0
	//   For forward reads: start from version 1 (beginning of stream).
	//   For reverse reads: start from the latest version (end of stream).
	AfterVersion int64

	// Count is the number of events to read.
	//
	// Default: 0 (read all events)
	Count int64

	// Direction is the direction to read the stream.
	//
	// Default: Forward
	Direction ReadStreamDirection
}

// A ReadStreamDirection specifies the direction in which to read a stream.
type ReadStreamDirection int

const (
	// Forward reads the stream from the beginning to the end.
	Forward ReadStreamDirection = iota

	// Reverse reads the stream from the end to the beginning.
	Reverse
)

// An StreamWriter appends events to an event stream.
type StreamWriter interface {
	// AppendStream appends events to an event stream.
	// The expected version of the stream can be specified in the options.
	//
	// On success, AppendStream returns the written events in append order, each
	// populated exactly as a subsequent read of the stream would return it: the
	// store-assigned ID, stream ID and version, timestamp, and global position
	// (where the backend has one), alongside the payload, content type, and
	// metadata as written.
	//
	// An append that returns an error may or may not have taken effect — a
	// store can commit and lose its response — with one exception: a
	// StreamVersionMismatchError is a refusal, returned only when nothing
	// was appended.
	AppendStream(ctx context.Context, streamID typeid.ID, events []*WritableEvent, opts AppendStreamOptions) ([]*Event, error)
}

// AppendStreamOptions are options for appending events to a stream.
type AppendStreamOptions struct {
	// ExpectVersion specifies the expected latest version of the stream
	// when appending events. When nil, no version check is performed.
	// When non-nil, the value is compared against the current stream version.
	//
	// Default: nil (no expectation)
	ExpectVersion *int64

	// StreamMustNotExist specifies that the stream must not already exist.
	// If the stream already exists (has any events), the append will fail
	// with a StreamVersionMismatchError.
	//
	// This field is mutually exclusive with ExpectVersion.
	//
	// Default: false
	StreamMustNotExist bool
}

// A GlobalReader reads events across all of a store's streams in the store's
// global order.
//
// GlobalReader is optional and deliberately not part of Store: a backend
// implements it only when it has a single ordering authority for everything it
// stores — one able to publish positions in commit-safe order, per ReadAll's
// stable-prefix contract — and one without such an authority is not forced to
// fake one. Callers discover support with a type assertion.
//
// Global reads are forward-only: they exist so read models and projections can
// consume history in order and resume from a position.
type GlobalReader interface {
	// ReadAll creates an iterator over events from all streams, in
	// ascending global order.
	//
	// Every event an implementation yields must carry a non-nil GlobalPosition,
	// and positions must be strictly increasing across the read — with gaps
	// allowed, since backends may consume positions they never commit. The
	// positions yielded here must be the same ones per-stream reads report for
	// the same events. eventstore/storetest enforces all of this.
	//
	// A read is finite: ReadAll linearizes exactly once, between invocation
	// and return, capturing a finite store frontier — an append overlapping
	// the call may land on either side of that point, but once ReadAll has
	// returned, the frontier is settled, however long the iteration lives.
	// Subject to AfterPosition, the iterator yields events through that
	// frontier, then reports ErrEndOfEventStream and remains exhausted; when
	// Count > 0, it yields at most the first Count matching events. A
	// consumer tails the store by reading again from the last position it
	// saw.
	//
	// Yielded positions form a stable prefix: once a read yields position P, no
	// later commit may introduce a previously unseen event at or below P.
	// Equivalently, a backend must publish positions in order — an event
	// becomes visible only once every lower position is settled, occupied by a
	// visible event or permanently dead, never still in flight. This is what
	// makes resuming after a yielded position gap-free and a drained read a
	// true caught-up-to-the-frontier observation; without it, positions are not
	// checkpoints. A backend that cannot promise this — say, one that allocates
	// positions before commit and lets commits land out of order — must not
	// advertise GlobalReader. eventstore/storetest enforces the fixed frontier;
	// commit ordering cannot be forced through this interface, so an
	// implementation that can separate position allocation from publication
	// must carry its own deterministic regression for it, and one that
	// publishes atomically must pin or document the mechanism that makes
	// ordering structural.
	//
	// A read with nothing to yield — an empty store, or a position at or past
	// the newest event — returns a valid iterator that immediately reports
	// ErrEndOfEventStream. ErrStreamNotFound has no place in a global read:
	// it answers whether an addressed stream exists, and a global read
	// addresses none.
	ReadAll(ctx context.Context, opts ReadAllOptions) (StreamIterator, error)
}

// ReadAllOptions are options for reading events across all streams.
type ReadAllOptions struct {
	// AfterPosition specifies the exclusive global position after which to
	// read: only events with GlobalPosition > AfterPosition are returned.
	//
	// The stable-prefix contract (see GlobalReader) is what makes resuming
	// from a previously yielded position gap-free: everything at or below it
	// is settled, so nothing can commit into the skipped range.
	//
	// Default: 0 (read from the beginning)
	AfterPosition int64

	// Count is the number of events to read.
	//
	// A positive Count truncates the frontier: a read exhausted after exactly
	// that many events says nothing about the store head — only an unbounded
	// read's exhaustion, or exhaustion short of Count, observes the frontier
	// itself.
	//
	// Default: 0 (read all events)
	Count int64
}

// A StreamDeleter deletes events from streams.
//
// StreamDeleter is optional and deliberately not part of Store: a backend
// implements it when its storage can remove committed events. Callers discover
// support with a type assertion.
type StreamDeleter interface {
	// DeleteStream deletes events from a stream. With zero options the entire
	// stream is deleted and its ID may be reused: a subsequent append starts a
	// new stream at version 1. With ToVersion set the stream is truncated
	// instead: events at or below ToVersion are removed, later events keep
	// their versions, and appends continue from the existing tip even when
	// truncation has emptied the stream. Deleting a stream that was never
	// written reports ErrStreamNotFound.
	DeleteStream(ctx context.Context, streamID typeid.ID, opts DeleteStreamOptions) error
}

// DeleteStreamOptions are options for deleting events from a stream.
type DeleteStreamOptions struct {
	// ToVersion is the inclusive upper bound of versions to delete, truncating
	// the stream rather than deleting it. A bound at or beyond the stream tip
	// empties the stream without deleting it.
	//
	// Default: 0 (delete the entire stream)
	ToVersion int64
}

// VersionPtr returns a pointer to the given version value.
// This is a convenience function for constructing AppendStreamOptions
// with a specific expected version.
func VersionPtr(v int64) *int64 {
	return &v
}

// ReservedMetadataPrefix is the event metadata key prefix reserved for estoria
// itself. Callers and backends must not write keys carrying it.
const ReservedMetadataPrefix = "estoria."

// ReservedStreamTypePrefix is the stream type prefix reserved for estoria's
// own infrastructure streams, such as projection rebuild aggregates. User
// aggregate types must not carry it; aggregatestore enforces this. The
// enforcement is a guardrail against accidental collision, not a trust
// boundary: callers own their event store and can write to any stream in it.
const ReservedStreamTypePrefix = "estoria."

// An Event is an event that has been read from an event store.
type Event struct {
	ID             typeid.ID
	StreamID       typeid.ID
	StreamVersion  int64
	GlobalPosition *int64
	Timestamp      time.Time
	Data           []byte

	// DataContentType is the MIME content type of Data, declared by the codec
	// that produced the bytes. Stores return it exactly as it was written; an
	// empty value means the event was written before its writer declared
	// content types, and carries whatever encoding that writer's codec produced.
	DataContentType string

	// Metadata is optional key-value metadata associated with the event.
	// Keys prefixed "estoria." are reserved for estoria itself.
	Metadata map[string]string
}

// A WritableEvent is an event that can be written to an event store.
type WritableEvent struct {
	Type string

	// Data is the serialized event data.
	Data []byte

	// DataContentType is the MIME content type of Data, declared by the codec
	// that produced the bytes. A backend that recognizes the type may store the
	// payload natively; one that does not treats the payload as opaque bytes.
	// Backends must round-trip the declaration verbatim, including an empty one:
	// the default lives with the codec layer, never with storage.
	DataContentType string

	// Metadata is optional key-value metadata associated with the event.
	// Keys prefixed "estoria." are reserved for estoria itself; callers and
	// backends must not write them.
	Metadata map[string]string
}

// EventMarshalingError is returned when an event fails to marshal.
type EventMarshalingError struct {
	StreamID typeid.ID
	EventID  typeid.ID
	Err      error
}

func (e EventMarshalingError) Error() string {
	return "marshaling event: " + e.Err.Error()
}

func (e EventMarshalingError) Unwrap() error {
	return e.Err
}

func (e EventMarshalingError) Is(target error) bool {
	_, ok := target.(EventMarshalingError)
	return ok
}

// EventUnmarshalingError is returned when an event fails to unmarshal.
type EventUnmarshalingError struct {
	StreamID typeid.ID
	EventID  typeid.ID
	Err      error
}

func (e EventUnmarshalingError) Error() string {
	return "unmarshaling event: " + e.Err.Error()
}

func (e EventUnmarshalingError) Unwrap() error {
	return e.Err
}

func (e EventUnmarshalingError) Is(target error) bool {
	_, ok := target.(EventUnmarshalingError)
	return ok
}

// StreamVersionMismatchError is returned when the expected stream version does not match
// the actual stream version. When StreamMustNotExist triggers this error, ExpectedVersion
// is set to 0, which is indistinguishable from an explicit ExpectVersion of 0.
//
// A mismatch is a refusal: implementations return it only when the append
// left the stream untouched, and callers may rely on nothing having been
// appended.
type StreamVersionMismatchError struct {
	StreamID        typeid.ID
	ExpectedVersion int64
	ActualVersion   int64
}

// Error returns the error message.
func (e StreamVersionMismatchError) Error() string {
	return fmt.Sprintf("stream version mismatch: expected version %d, got version %d",
		e.ExpectedVersion,
		e.ActualVersion)
}

// Is returns true if the target is a StreamVersionMismatchError.
func (e StreamVersionMismatchError) Is(target error) bool {
	_, ok := target.(StreamVersionMismatchError)
	return ok
}

// InitializationError is returned when an event store fails to initialize.
type InitializationError struct {
	Err error
}

// Error returns the error message.
func (e InitializationError) Error() string {
	return "initializing event store: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e InitializationError) Unwrap() error {
	return e.Err
}

// Is returns true if the target is an InitializationError.
func (e InitializationError) Is(target error) bool {
	_, ok := target.(InitializationError)
	return ok
}

// ErrStreamNotFound is returned when an event stream is not found. It signifies an absent
// stream, not a read whose options matched no events; see StreamReader.ReadStream.
var ErrStreamNotFound = errors.New("stream not found")

// ErrStreamIteratorClosed is returned when an operation is attempted on a closed stream iterator.
var ErrStreamIteratorClosed = errors.New("stream iterator closed")

// ErrEndOfEventStream is returned by a stream iterator when there are no more events in the stream.
var ErrEndOfEventStream = errors.New("end of event stream")

// Collect reads the given stream iterator to the end of the stream, collecting
// the events into a slice. On an iteration error it returns the events read so far
// alongside the error.
func Collect(ctx context.Context, iter StreamIterator) ([]*Event, error) {
	events := []*Event{}
	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, ErrEndOfEventStream) {
			break
		} else if err != nil {
			return events, fmt.Errorf("reading event: %w", err)
		}

		events = append(events, event)
	}

	return events, nil
}
