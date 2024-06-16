package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
	SetCompletedAt(handlerName string, at time.Time)
}

type HandlerResult struct {
	CompletedAt time.Time
	Error       error
}

func (r HandlerResult) String() string {
	if r.Error != nil {
		return fmt.Sprintf("error: %s", r.Error)
	}

	return fmt.Sprintf("completed at: %s", r.CompletedAt)
}

type ItemHandler interface {
	Name() string
	Handle(ctx context.Context, event OutboxItem) error
}

type Processor struct {
	outbox   Outbox
	handlers map[string]ItemHandler // handler name -> handler
	stop     context.CancelFunc
}

func NewProcessor(outbox Outbox) *Processor {
	return &Processor{
		outbox:   outbox,
		handlers: make(map[string]ItemHandler),
	}
}

func (p *Processor) RegisterHandlers(handlers ...ItemHandler) {
	for _, handler := range handlers {
		p.handlers[handler.Name()] = handler
	}
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

	slog.Info("starting outbox processor", "handlers", len(p.handlers))

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

	itemHandlers := entry.Handlers()
	errorCount := 0
	for handlerName, handlerResult := range itemHandlers {
		if !handlerResult.CompletedAt.IsZero() {
			continue
		}

		handler, ok := p.handlers[handlerName]
		if ok {
			if err := handler.Handle(ctx, entry); err != nil {
				slog.Error("handling outbox item", "handler", handler.Name(), "error", err)
				entry.SetHandlerError(handler.Name(), err)
				errorCount++
			} else {
				slog.Info("handled outbox item", "handler", handler.Name(), "event_type", entry.EventID().TypeName())
				entry.SetCompletedAt(handler.Name(), time.Now())
			}
		} else {
			slog.Info("no handler found for outbox item", "handler", handlerName, "event_id", entry.EventID())
		}
	}

	slog.Info("entry handled", "event_id", entry.EventID(), "errors", errorCount)

	return nil
}

func (p *Processor) run(ctx context.Context, iterator Iterator) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("stopping outbox processor")
			return
		default:
		}

		slog.Info("polling for next outbox entry")
		entry, err := iterator.Next(ctx)
		if err != nil {
			slog.Error("reading outbox entry", "error", err)
			p.Stop()
		}

		if entry != nil {
			slog.Info("found outbox entry", "event_id", entry.EventID())
			if err := p.Handle(ctx, entry); err != nil {
				slog.Error("handling outbox entry", "error", err)
			} else {
				slog.Info("handled outbox entry", "event_id", entry.EventID())
			}
		} else {
			<-time.After(time.Second)
		}
	}
}
