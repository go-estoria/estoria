package eventwriter

import (
	"context"
	"fmt"

	"github.com/jefflinse/continuum"
)

type MemoryWriter struct {
	Store []continuum.Event
}

func (r MemoryWriter) WriteEvent(_ context.Context, event continuum.Event) error {
	for _, e := range r.Store {
		if e.EventID() == event.EventID() {
			return ErrEventExists{
				EventID: event.EventID(),
			}
		}
	}

	r.Store = append(r.Store, event)
	return nil
}

type ErrEventExists struct {
	EventID continuum.Identifier
}

func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
