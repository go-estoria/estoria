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
	ReadStream(ctx context.Context, id typeid.UUID, opts ReadStreamOptions) (StreamIterator, error)
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
	// Offset is the starting position in the stream (exclusive).
	//
	// Default: 0 (beginning of stream)
	Offset int64

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
	AppendStream(ctx context.Context, streamID typeid.UUID, events []*WritableEvent, opts AppendStreamOptions) error
}

// AppendStreamOptions are options for appending events to a stream.
type AppendStreamOptions struct {
	// ExpectVersion specifies the expected latest version of the stream
	// when appending events.
	//
	// Default: 0 (no expectation)
	ExpectVersion int64
}

// An Event is an event that has been read from an event store.
type Event struct {
	ID            typeid.UUID
	StreamID      typeid.UUID
	StreamVersion int64
	Timestamp     time.Time
	Data          []byte
}

// A WritableEvent is an event that can be written to an event store.
type WritableEvent struct {
	ID typeid.UUID

	// Timestamp is the time the event occurred. If zero (default), the current time is used.
	Timestamp time.Time

	// Data is the serialized event data.
	Data []byte
}

type EventMarshalingError struct {
	StreamID typeid.UUID
	EventID  typeid.UUID
	Err      error
}

func (e EventMarshalingError) Error() string {
	return "marshaling event: " + e.Err.Error()
}

func (e EventMarshalingError) Unwrap() error {
	return e.Err
}

type EventUnmarshalingError struct {
	StreamID typeid.UUID
	EventID  typeid.UUID
	Err      error
}

func (e EventUnmarshalingError) Error() string {
	return "unmarshaling event: " + e.Err.Error()
}

func (e EventUnmarshalingError) Unwrap() error {
	return e.Err
}

// StreamVersionMismatchError is returned when the expected stream version does not match the actual stream version.
type StreamVersionMismatchError struct {
	StreamID        typeid.UUID
	EventID         typeid.UUID
	ExpectedVersion int64
	ActualVersion   int64
}

// Error returns the error message.
func (e StreamVersionMismatchError) Error() string {
	return fmt.Sprintf("stream version mismatch: expected version %d, got version %d",
		e.ExpectedVersion,
		e.ActualVersion)
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

// ErrStreamNotFound is returned when an event stream is not found.
var ErrStreamNotFound = errors.New("stream not found")

// ErrStreamIteratorClosed is returned when an operation is attempted on a closed stream iterator.
var ErrStreamIteratorClosed = errors.New("stream iterator closed")

// ErrEndOfEventStream is returned by a stream iterator when there are no more events in the stream.
var ErrEndOfEventStream = errors.New("end of event stream")

func ReadAll(ctx context.Context, iter StreamIterator) ([]*Event, error) {
	events := []*Event{}
	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, ErrEndOfEventStream) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("reading event: %w", err)
		}

		events = append(events, event)
	}

	return events, nil
}
