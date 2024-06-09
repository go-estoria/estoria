package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type Outbox interface {
	Iterator() (Iterator, error)
}

type Iterator interface {
	Next(ctx context.Context) (OutboxEntry, error)
}

type OutboxEntry interface {
	Timestamp() time.Time
	StreamID() typeid.AnyID
	EventID() typeid.AnyID
	EventData() []byte
}

type Handler interface {
	Handle(ctx context.Context, event OutboxEntry) error
}

type Processor struct {
	outbox   Outbox
	handlers map[string][]Handler // event type -> handlers
	stop     context.CancelFunc
}

func NewProcessor(outbox Outbox) *Processor {
	return &Processor{
		outbox:   outbox,
		handlers: make(map[string][]Handler),
	}
}

func (p *Processor) RegisterHandlers(eventType estoria.EntityEventData, handlers ...Handler) {
	p.handlers[eventType.EventType()] = append(p.handlers[eventType.EventType()], handlers...)
}

func (p *Processor) Start(ctx context.Context) error {
	iterator, err := p.outbox.Iterator()
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

func (p *Processor) Handle(ctx context.Context, entry OutboxEntry) error {
	handlers, ok := p.handlers[entry.EventID().Prefix()]
	if !ok {
		slog.Debug("no outbox handlers for event type", "event_type", entry.EventID().Prefix())
		return nil
	}

	for _, handler := range handlers {
		if err := handler.Handle(ctx, entry); err != nil {
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

		entry, err := iterator.Next(ctx)
		if err != nil {
			slog.Error("reading outbox entry", "error", err)
			p.Stop()
		}

		if err := p.Handle(ctx, entry); err != nil {
			slog.Error("handling outbox entry", "error", err)
			p.Stop()
		}
	}
}
