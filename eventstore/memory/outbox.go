package memory

import (
	"context"
	"log/slog"
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
	handlers map[string][]outbox.ItemHandler // event type -> handlers
	mu       sync.RWMutex
}

// NewOutbox creates a new in-memory outbox.
func NewOutbox() *Outbox {
	return &Outbox{
		items:    make([]outbox.OutboxItem, 0),
		handlers: make(map[string][]outbox.ItemHandler),
	}
}

// RegisterHandlers registers handlers for a specific event type.
func (o *Outbox) RegisterHandlers(eventType estoria.EntityEventData, handlers ...outbox.ItemHandler) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.handlers[eventType.EventType()] = append(o.handlers[eventType.EventType()], handlers...)
}

// HandleEvents adds an outbox item for each event.
func (o *Outbox) HandleEvents(ctx context.Context, events []estoria.EventStoreEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	slog.Debug("inserting events into outbox", "tx", "inherited", "events", len(events))

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

		for _, handler := range o.handlers[event.ID().TypeName()] {
			item.handlers[handler.Name()] = outbox.HandlerResult{}
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

	slog.Debug("iterating outbox", "outbox_len", len(i.outbox.items), "cursor", i.cursor, "match_event_types", i.matchEventTypes)
	var next outbox.OutboxItem
	for _, item := range i.outbox.items {
		item.Lock()
		for _, eventType := range i.matchEventTypes {
			handlers := item.Handlers()
			slog.Debug("checking outbox item", "handlers", handlers)
			if _, ok := handlers[eventType]; ok {
				i.outbox.mu.Unlock()
				slog.Debug("found outbox item", "id", "stream_id", item.StreamID(), "event_id", item.EventID(), "event_data", item.EventData(), "handlers", item.Handlers())
				next = item
				break
			}
		}
		item.Unlock()
	}

	i.outbox.mu.Unlock()

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
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[handlerName] = outbox.HandlerResult{
		Error: err,
	}
}

type handlerResult struct {
	processed bool
	error     error
}
