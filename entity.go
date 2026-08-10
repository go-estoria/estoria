package estoria

import (
	"encoding/json"

	"github.com/gofrs/uuid/v5"
)

// A StateFactory creates a new instance of an aggregate's state type S.
// The UUID identifies the aggregate the state belongs to; the state may record it,
// but is not required to.
type StateFactory[S any] func(id uuid.UUID) S

// ContentTypeJSON is the content type declared by the JSON codecs.
const ContentTypeJSON = "application/json"

// StateCodec is an interface for marshaling aggregate state to and from bytes.
type StateCodec[S any] interface {
	// MarshalState marshals state to bytes.
	MarshalState(state S) ([]byte, error)
	// UnmarshalState unmarshals state from bytes.
	UnmarshalState(data []byte, dest *S) error
	// ContentType returns the MIME content type of the bytes the codec produces
	// and consumes. It is declared on the payloads the codec encodes, so that
	// storage backends can act on the encoding without sniffing bytes.
	ContentType() string
}

// JSONStateCodec is a StateCodec that encodes state as JSON.
type JSONStateCodec[S any] struct{}

var _ StateCodec[struct{}] = JSONStateCodec[struct{}]{}

func (c JSONStateCodec[S]) MarshalState(state S) ([]byte, error) {
	return json.Marshal(state)
}

func (c JSONStateCodec[S]) UnmarshalState(data []byte, dest *S) error {
	return json.Unmarshal(data, dest)
}

func (c JSONStateCodec[S]) ContentType() string {
	return ContentTypeJSON
}

// A DomainEvent is an event that can be applied to an aggregate's state to produce
// the next state.
type DomainEvent[S any] interface {
	// EventType returns the type of event.
	EventType() string
	// New returns a new instance of the event.
	New() DomainEvent[S]
	// ApplyTo applies the event to state, returning the new state.
	//
	// ApplyTo is total: a persisted event is a fact, and applying one cannot fail.
	// Validate commands before appending events; a payload that cannot be decoded
	// surfaces as a hydration error before ApplyTo is ever reached.
	ApplyTo(state S) S
}

// DomainEventCodec is an interface for marshaling domain events to and from bytes.
type DomainEventCodec[S any] interface {
	// MarshalDomainEvent marshals a domain event to bytes.
	MarshalDomainEvent(event DomainEvent[S]) ([]byte, error)
	// UnmarshalDomainEvent unmarshals a domain event from bytes.
	UnmarshalDomainEvent(data []byte, dest DomainEvent[S]) error
	// ContentType returns the MIME content type of the bytes the codec produces
	// and consumes. It is declared on the payloads the codec encodes, so that
	// storage backends can act on the encoding without sniffing bytes.
	ContentType() string
}

// JSONDomainEventCodec is a DomainEventCodec that encodes domain events as JSON.
type JSONDomainEventCodec[S any] struct{}

var _ DomainEventCodec[struct{}] = JSONDomainEventCodec[struct{}]{}

func (c JSONDomainEventCodec[S]) MarshalDomainEvent(event DomainEvent[S]) ([]byte, error) {
	return json.Marshal(event)
}

func (c JSONDomainEventCodec[S]) UnmarshalDomainEvent(data []byte, dest DomainEvent[S]) error {
	return json.Unmarshal(data, dest)
}

func (c JSONDomainEventCodec[S]) ContentType() string {
	return ContentTypeJSON
}
