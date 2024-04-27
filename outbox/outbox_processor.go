package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type OutboxEntry interface {
	Timestamp() time.Time
	StreamID() typeid.AnyID
	EventID() typeid.AnyID
	EventData() []byte
}

type Iterator interface {
	Next(ctx context.Context) (OutboxEntry, error)
}

type Handler interface {
	Handle(event OutboxEntry) error
}

type Processor struct {
	iterator Iterator
	handlers map[string][]Handler // event type -> handlers
	stopped  chan struct{}
}

func NewProcessor(iterator Iterator) *Processor {
	return &Processor{
		iterator: iterator,
		handlers: make(map[string][]Handler),
		stopped:  nil,
	}
}

func (p *Processor) RegisterHandlers(eventType estoria.EventData, handlers ...Handler) {
	p.handlers[eventType.EventType()] = append(p.handlers[eventType.EventType()], handlers...)
}

func (p *Processor) Start(ctx context.Context) error {
	slog.Debug("starting outbox processor", "handlers", len(p.handlers))
	p.stopped = make(chan struct{})
	go p.run(ctx)
	return nil
}

func (p *Processor) Stop() {
	close(p.stopped)
}

func (p *Processor) Handle(entry OutboxEntry) error {
	handlers, ok := p.handlers[entry.EventID().Prefix()]
	if !ok {
		slog.Debug("no outbox handlers for event type", "event_type", entry.EventID().Prefix())
		return nil
	}

	for _, handler := range handlers {
		if err := handler.Handle(entry); err != nil {
			return fmt.Errorf("handling outbox entry: %w", err)
		}
	}

	return nil
}

func (p Processor) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			slog.Debug("context done, stopping outbox processor")
			return
		case <-p.stopped:
			slog.Debug("stopping outbox processor")
			return
		default:
		}

		entry, err := p.iterator.Next(ctx)
		if err != nil {
			slog.Error("reading outbox entry", "error", err)
			p.Stop()
		}

		if err := p.Handle(entry); err != nil {
			slog.Error("handling outbox entry", "error", err)
			p.Stop()
		}
	}
}
