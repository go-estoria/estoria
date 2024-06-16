package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/outbox"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// Outbox is an in-memory outbox for use with the in-memory event store.
type Outbox struct {
	items    []outbox.OutboxItem
	handlers map[string][]string // event type -> []handlernames
	mu       sync.RWMutex
}

// NewOutbox creates a new in-memory outbox.
func NewOutbox() *Outbox {
	return &Outbox{
		items:    make([]outbox.OutboxItem, 0),
		handlers: make(map[string][]string), // event type -> handlers
	}
}

// RegisterHandlers registers handlers for a specific event type.
// The handlers names are associated with the event type and added to the outbox items,
// so that one or more outbox processors can track and process the items.
func (o *Outbox) RegisterHandlers(eventType estoria.EntityEventData, handlers ...outbox.ItemHandler) {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, handler := range handlers {
		o.handlers[eventType.EventType()] = append(o.handlers[eventType.EventType()], handler.Name())
	}
}

// HandleEvents adds an outbox item for each event.
func (o *Outbox) HandleEvents(ctx context.Context, events []estoria.EventStoreEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	slog.Info("inserting events into outbox", "tx", "inherited", "events", len(events))

	for _, event := range events {
		item := &outboxItem{
			id:        uuid.Must(uuid.NewV7()),
			streamID:  event.StreamID(),
			eventID:   event.ID(),
			eventData: event.Data(),
			handlers:  make(map[string]outbox.HandlerResult),
			createdAt: time.Now(),
			updatedAt: time.Now(),
		}

		// for each handler name for this event type, add a handler result to track processing
		for _, handler := range o.handlers[event.ID().TypeName()] {
			item.handlers[handler] = outbox.HandlerResult{}
		}

		o.items = append(o.items, item)
	}

	return nil
}

// Iterator returns an iterator for the outbox.
func (o *Outbox) Iterator(matchEventTypes []string) (outbox.Iterator, error) {
	return &OutboxIterator{
		outbox:          o,
		cursor:          0,
		matchEventTypes: matchEventTypes,
	}, nil
}

// An OutboxIterator is an iterator for the outbox.
type OutboxIterator struct {
	outbox          *Outbox
	cursor          int
	matchEventTypes []string
}

// Next returns the next outbox entry.
func (i *OutboxIterator) Next(ctx context.Context) (outbox.OutboxItem, error) {
	i.outbox.mu.Lock()
	defer i.outbox.mu.Unlock()
	slog.Info("iterating outbox", "cursor", i.cursor, "items", len(i.outbox.items))

	if i.cursor >= len(i.outbox.items) {
		slog.Info("cursor is at end of outbox")
		return nil, nil
	}

	var next outbox.OutboxItem
	for _, item := range i.outbox.items[i.cursor:] {
		item.Lock()
		handlers := item.Handlers()
		for _, result := range handlers {
			if !result.CompletedAt.IsZero() {
				slog.Info("skipping completed outbox item", "event_id", item.EventID(), "handlers", item.Handlers())
				continue
			}

			for _, match := range i.matchEventTypes {
				if _, ok := handlers[match]; ok {
					slog.Info("matched outbox item", "event_id", item.EventID(), "handlers", item.Handlers())
					next = item
					break
				}
			}

			if next != nil {
				// we didn't match any event types, so skip this item
				break
			}
		}

		item.Unlock()

		i.cursor++
		if next != nil {
			break
		}
	}

	return next, nil
}

type outboxItem struct {
	id        uuid.UUID
	streamID  typeid.TypeID
	eventID   typeid.UUID
	eventData []byte
	handlers  map[string]outbox.HandlerResult
	createdAt time.Time
	updatedAt time.Time
	mu        sync.RWMutex
}

func (e *outboxItem) StreamID() typeid.TypeID {
	return e.streamID
}

func (e *outboxItem) EventID() typeid.UUID {
	return e.eventID
}

func (e *outboxItem) EventData() []byte {
	return e.eventData
}

func (e *outboxItem) Handlers() map[string]outbox.HandlerResult {
	return e.handlers
}

func (e *outboxItem) Lock() {
	e.mu.Lock()
}

func (e *outboxItem) Unlock() {
	e.mu.Unlock()
}

func (e *outboxItem) SetHandlerError(handlerName string, err error) {
	e.handlers[handlerName] = outbox.HandlerResult{
		Error: err,
	}
}

func (e *outboxItem) SetCompletedAt(handlerName string, at time.Time) {
	e.handlers[handlerName] = outbox.HandlerResult{
		CompletedAt: at,
	}
}

func (e *outboxItem) String() string {
	handlerNames := make([]string, 0, len(e.handlers))
	for name := range e.handlers {
		handlerNames = append(handlerNames, name)
	}

	return fmt.Sprintf("%s: %s", e.EventID().TypeName(), strings.Join(handlerNames, ", "))
}
