package eventreader

import (
	"context"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// MemoryReader is an EventReader that reads events from an in-memory store.
type MemoryReader struct {
	Store *[]continuum.Event
}

// ReadEvents reads events from the in-memory store.
func (r MemoryReader) ReadEvents(_ context.Context, aggregateID continuum.AggregateID) ([]continuum.Event, error) {
	events := []continuum.Event{}
	for _, event := range *r.Store {
		if event.AggregateID().Equals(aggregateID) {
			slog.Default().WithGroup("eventreader").Debug("reading event", "event_id", event.ID())
			events = append(events, event)
		}
	}

	return events, nil
}
