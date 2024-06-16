package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

type Outbox interface {
	Iterator(matchEventTypes []string) (Iterator, error)
}

type Iterator interface {
	Next(ctx context.Context) (OutboxItem, error)
}

type OutboxItem interface {
	StreamID() typeid.TypeID
	EventID() typeid.UUID
	EventData() []byte
	Handlers() map[string]HandlerResult
	Lock()
	Unlock()
	SetHandlerError(handlerName string, err error)
}

type HandlerResult struct {
	CompletedAt time.Time
	Error       error
}

type ItemHandler interface {
	Name() string
	Handle(ctx context.Context, event OutboxItem) error
}

type Processor struct {
	outbox   Outbox
	handlers map[string][]ItemHandler // event type -> handlers
	stop     context.CancelFunc
}

func NewProcessor(outbox Outbox) *Processor {
	return &Processor{
		outbox:   outbox,
		handlers: make(map[string][]ItemHandler),
	}
}

func (p *Processor) RegisterHandlers(eventType estoria.EntityEventData, handlers ...ItemHandler) {
	p.handlers[eventType.EventType()] = append(p.handlers[eventType.EventType()], handlers...)
}

func (p *Processor) Start(ctx context.Context) error {
	eventTypes := make([]string, 0, len(p.handlers))
	for eventType := range p.handlers {
		eventTypes = append(eventTypes, eventType)
	}

	iterator, err := p.outbox.Iterator(eventTypes)
	if err != nil {
		return fmt.Errorf("creating outbox iterator: %w", err)
	}

	slog.Debug("starting outbox processor", "handlers", len(p.handlers))

	ctx, cancel := context.WithCancel(ctx)
	p.stop = cancel
	go p.run(ctx, iterator)
	return nil
}

func (p *Processor) Stop() {
	p.stop()
}

func (p *Processor) Handle(ctx context.Context, entry OutboxItem) error {
	entry.Lock()
	defer entry.Unlock()

	handlers, ok := p.handlers[entry.EventID().TypeName()]
	if !ok {
		slog.Debug("no outbox handlers for event type", "event_type", entry.EventID().TypeName())
		return nil
	}

	for _, handler := range handlers {
		if err := handler.Handle(ctx, entry); err != nil {
			entry.SetHandlerError(handler.Name(), err)
			return fmt.Errorf("handling outbox entry: %w", err)
		}
	}

	return nil
}

func (p *Processor) run(ctx context.Context, iterator Iterator) {
	for {
		select {
		case <-ctx.Done():
			slog.Debug("stopping outbox processor")
			return
		default:
		}

		slog.Debug("getting next outbox entry")
		entry, err := iterator.Next(ctx)
		if err != nil {
			slog.Error("reading outbox entry", "error", err)
			p.Stop()
		}

		if entry != nil {
			if err := p.Handle(ctx, entry); err != nil {
				slog.Error("handling outbox entry", "error", err)
			}
		} else {
			<-time.After(time.Second)
		}
	}
}
