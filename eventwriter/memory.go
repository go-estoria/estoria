package eventwriter

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// MemoryWriter is an EventWriter that writes events to an in-memory store.
type MemoryWriter struct {
	Store *[]continuum.Event
}

// WriteEvent writes an event to the in-memory store.
func (r *MemoryWriter) WriteEvent(_ context.Context, event continuum.Event) error {
	for _, e := range *r.Store {
		if eventID := event.ID(); e.ID().Equals(eventID) {
			return ErrEventExists{
				EventID: eventID,
			}
		}
	}

	slog.Default().WithGroup("eventwriter").Debug("writing event", "event_id", event.ID())

	*r.Store = append(*r.Store, event)
	return nil
}

// ErrEventExists is returned when attempting to write an event that already exists.
type ErrEventExists struct {
	EventID continuum.EventID
}

// Error returns the error message.
func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
