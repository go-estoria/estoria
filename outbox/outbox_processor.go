package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type Outbox interface {
	Iterator() (Iterator, error)
	MarkHandled(ctx context.Context, itemID uuid.UUID, result HandlerResult) error
}

type Iterator interface {
	Next(ctx context.Context) (OutboxItem, error)
}

type OutboxItem interface {
	ID() uuid.UUID
	StreamID() typeid.TypeID
	EventID() typeid.UUID
	EventData() []byte
	Handlers() HandlerResultMap
	FullyProcessed() bool
}

type HandlerResult struct {
	HandlerName string
	CompletedAt time.Time
	Error       error
}

type HandlerResultMap map[string]*HandlerResult

func (m HandlerResultMap) IncompleteHandlers() []string {
	var handlers []string
	for handlerName, result := range m {
		if result.Error != nil || result.CompletedAt.IsZero() {
			handlers = append(handlers, handlerName)
		}
	}

	return handlers
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
	iterator, err := p.outbox.Iterator()
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
	if entry.FullyProcessed() {
		slog.Info("nothing to process", "event_id", entry.EventID())
		return nil
	}

	handlers := entry.Handlers()
	remainingHandlerNames := handlers.IncompleteHandlers()
	for _, handlerName := range remainingHandlerNames {
		if !handlers[handlerName].CompletedAt.IsZero() {
			continue
		}

		handler, ok := p.handlers[handlerName]
		if !ok {
			slog.Warn("no handler found for outbox item", "handler", handlerName, "event_id", entry.EventID())
			continue
		}

		if err := handler.Handle(ctx, entry); err != nil {
			slog.Error("handling outbox item", "handler", handler.Name(), "error", err)
			p.outbox.MarkHandled(ctx, entry.ID(), HandlerResult{
				HandlerName: handler.Name(),
				Error:       err,
			})
		} else {
			p.outbox.MarkHandled(ctx, entry.ID(), HandlerResult{
				HandlerName: handler.Name(),
				CompletedAt: time.Now(),
			})
		}
	}

	return nil
}

func (p *Processor) run(ctx context.Context, iterator Iterator) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("stopping outbox processor", "reason", ctx.Err())
			return
		default:
		}

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
