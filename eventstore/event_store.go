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
	AppendStream(ctx context.Context, streamID typeid.ID, events []*WritableEvent, opts AppendStreamOptions) error
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

// VersionPtr returns a pointer to the given version value.
// This is a convenience function for constructing AppendStreamOptions
// with a specific expected version.
func VersionPtr(v int64) *int64 {
	return &v
}

// An Event is an event that has been read from an event store.
type Event struct {
	ID             typeid.ID
	StreamID       typeid.ID
	StreamVersion  int64
	GlobalPosition *int64
	Timestamp      time.Time
	Data           []byte
	Metadata       map[string]string
}

// A WritableEvent is an event that can be written to an event store.
type WritableEvent struct {
	Type string

	// Data is the serialized event data.
	Data []byte

	// Metadata is optional key-value metadata associated with the event.
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

// EventExistsError is returned when an event with the same ID already exists.
type EventExistsError struct {
	EventID typeid.ID

	// Err is the underlying error from the storage backend (e.g., a database constraint
	// violation). It may be nil if the duplicate was detected at the application level.
	Err error
}

// Error returns the error message.
func (e EventExistsError) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}

// Unwrap returns the underlying error.
func (e EventExistsError) Unwrap() error {
	return e.Err
}

// Is returns true if the target is an EventExistsError.
func (e EventExistsError) Is(target error) bool {
	_, ok := target.(EventExistsError)
	return ok
}

// ErrStreamNotFound is returned when an event stream is not found.
var ErrStreamNotFound = errors.New("stream not found")

// ErrStreamIteratorClosed is returned when an operation is attempted on a closed stream iterator.
var ErrStreamIteratorClosed = errors.New("stream iterator closed")

// ErrEndOfEventStream is returned by a stream iterator when there are no more events in the stream.
var ErrEndOfEventStream = errors.New("end of event stream")

// ReadAll reads all events from the given stream iterator until it reaches the end of the stream
// or encounters an error. It returns a slice of events and any error encountered.
func ReadAll(ctx context.Context, iter StreamIterator) ([]*Event, error) {
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
