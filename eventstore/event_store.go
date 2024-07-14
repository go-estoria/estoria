package eventstore

import "errors"

// ErrStreamNotFound is returned when a stream is not found.
var ErrStreamNotFound = errors.New("stream not found")
