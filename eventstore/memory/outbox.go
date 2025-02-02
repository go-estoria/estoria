package memory

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/outbox"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// Outbox is an in-memory outbox for use with the in-memory event store.
type Outbox struct {
	items    []outbox.Item
	handlers map[string][]string // event type -> []handlernames
	mu       sync.RWMutex
}

// NewOutbox creates a new in-memory outbox.
func NewOutbox() *Outbox {
	return &Outbox{
		items:    make([]outbox.Item, 0),
		handlers: make(map[string][]string), // event type -> handlers
	}
}

// RegisterHandlers registers handlers for a specific event type.
// The handlers names are associated with the event type and added to the outbox items,
// so that one or more outbox processors can track and process the items.
func (o *Outbox) RegisterHandlers(eventType string, handlers ...outbox.ItemHandler) {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, handler := range handlers {
		o.handlers[eventType] = append(o.handlers[eventType], handler.Name())
	}
}

// HandleEvents adds an outbox item for each event.
func (o *Outbox) HandleEvents(_ context.Context, events []*eventstore.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	estoria.GetLogger().Debug("inserting events into outbox", "tx", "inherited", "events", len(events))

	for _, event := range events {
		item := &outboxItem{
			id:        uuid.Must(uuid.NewV7()),
			streamID:  event.StreamID,
			eventID:   event.ID,
			eventData: event.Data,
			handlers:  make(outbox.HandlerResultMap),
		}

		// for each handler name for this event type, add a handler result to track processing
		for _, handler := range o.handlers[event.ID.TypeName()] {
			item.handlers[handler] = &outbox.HandlerResult{}
		}

		o.items = append(o.items, item)
	}
}

// MarkHandled updates the handler result for the outbox item.
func (o *Outbox) MarkHandled(_ context.Context, itemID uuid.UUID, result outbox.HandlerResult) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, item := range o.items {
		if item.ID() == itemID {
			handlers := item.Handlers()
			for name, handler := range handlers {
				if name == result.HandlerName {
					handler.CompletedAt = result.CompletedAt
					handler.Error = result.Error
					return nil
				}
			}

			return fmt.Errorf("handler not registered for outbox item: %s", result.HandlerName)
		}
	}

	return fmt.Errorf("item not found: %s", itemID)
}

// Iterator returns an iterator for the outbox.
func (o *Outbox) Iterator() (outbox.Iterator, error) {
	return &OutboxIterator{
		outbox: o,
		cursor: 0,
	}, nil
}

// An OutboxIterator is an iterator for the outbox.
type OutboxIterator struct {
	outbox *Outbox
	cursor int
}

// Next returns the next outbox entry.
func (i *OutboxIterator) Next(_ context.Context) (outbox.Item, error) {
	i.outbox.mu.Lock()
	defer i.outbox.mu.Unlock()

	for ; i.cursor < len(i.outbox.items) && i.outbox.items[i.cursor].FullyProcessed(); i.cursor++ {
		// skip items that have been fully processed
		estoria.GetLogger().Debug("skipping fully processed outbox item", "event_id", i.outbox.items[i.cursor].EventID())
	}

	if i.cursor >= len(i.outbox.items) {
		return nil, io.EOF
	}

	item := i.outbox.items[i.cursor]

	i.cursor++
	return item, nil
}

type outboxItem struct {
	id        uuid.UUID
	streamID  typeid.UUID
	eventID   typeid.UUID
	eventData []byte
	handlers  outbox.HandlerResultMap
}

func (e *outboxItem) ID() uuid.UUID {
	return e.id
}

func (e *outboxItem) StreamID() typeid.UUID {
	return e.streamID
}

func (e *outboxItem) EventID() typeid.UUID {
	return e.eventID
}

func (e *outboxItem) EventData() []byte {
	return e.eventData
}

func (e *outboxItem) Handlers() outbox.HandlerResultMap {
	return e.handlers
}

func (e *outboxItem) String() string {
	handlerNames := make([]string, 0, len(e.handlers))
	for name := range e.handlers {
		handlerNames = append(handlerNames, name)
	}

	return fmt.Sprintf("%s: %s", e.EventID().TypeName(), strings.Join(handlerNames, ", "))
}

func (e *outboxItem) FullyProcessed() bool {
	return len(e.handlers.IncompleteHandlers()) == 0
}
