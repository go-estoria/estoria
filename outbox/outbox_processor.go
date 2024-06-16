package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

type Outbox interface {
	Iterator() (Iterator, error)
}

type Iterator interface {
	Next(ctx context.Context) (OutboxItem, error)
}

type OutboxItem interface {
	StreamID() typeid.TypeID
	EventID() typeid.UUID
	EventData() []byte
	Handlers() HandlerResultMap
	Lock()
	Unlock()
	SetHandlerError(handlerName string, err error)
	SetCompletedAt(handlerName string, at time.Time)
}

type HandlerResult struct {
	CompletedAt time.Time
	Error       error
}

type HandlerResultMap map[string]*HandlerResult

func (m HandlerResultMap) FullyProcessed() bool {
	if len(m) == 0 {
		return true
	}

	for _, result := range m {
		if result.Error != nil || result.CompletedAt.IsZero() {
			return false
		}
	}

	return true
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
	entry.Lock()
	defer entry.Unlock()

	if entry.Handlers().FullyProcessed() {
		slog.Info("nothing to process", "event_id", entry.EventID())
		return nil
	}

	handlers := entry.Handlers()
	errorCount := 0
	for handlerName, handlerResult := range handlers {
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
				entry.SetCompletedAt(handler.Name(), time.Now())
			}
		} else {
			slog.Warn("no handler found for outbox item", "handler", handlerName, "event_id", entry.EventID())
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
