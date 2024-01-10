package eventreader

import (
	"context"
	"log/slog"

	"github.com/jefflinse/continuum"
)

type MemoryReader struct {
	Store *[]continuum.Event
}

func (r MemoryReader) ReadEvents(_ context.Context, aggregateID continuum.AggregateID) ([]continuum.Event, error) {
	events := []continuum.Event{}
	for _, event := range *r.Store {
		if event.AggregateID().Equals(aggregateID) {
			slog.Default().WithGroup("eventreader").Debug("reading event", "aggregate_id", aggregateID, "event_id", event.EventID())
			events = append(events, event)
		}
	}

	return events, nil
}
