package eventstore

import (
	"context"
	"errors"
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
	// Next reads the next event from the stream. It returns io.EOF when there are no more events.
	Next(ctx context.Context) (*EventStoreEvent, error)
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
	AppendStream(ctx context.Context, streamID typeid.UUID, events []*EventStoreEvent, opts AppendStreamOptions) error
}

// AppendStreamOptions are options for appending events to a stream.
type AppendStreamOptions struct {
	// ExpectVersion specifies the expected latest version of the stream
	// when appending events.
	//
	// Default: 0 (no expectation)
	ExpectVersion int64
}

// ErrStreamVersionMismatch is returned when the expected stream version does not match the actual stream version.
var ErrStreamVersionMismatch = errors.New("stream version mismatch")

// An EventStoreEvent can be appended to and loaded from an event store.
type EventStoreEvent struct {
	ID            typeid.UUID
	StreamID      typeid.UUID
	StreamVersion int64
	Timestamp     time.Time
	Data          []byte
}

// ErrStreamNotFound is returned when a stream is not found.
var ErrStreamNotFound = errors.New("stream not found")
