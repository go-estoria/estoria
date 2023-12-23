package memory

import (
	"github.com/jefflinse/continuum"
	memoryeventreader "github.com/jefflinse/continuum/eventreader/memory"
	memoryeventwriter "github.com/jefflinse/continuum/eventwriter/memory"
)

func NewEventStore() *continuum.EventStore {
	events := make(continuum.EventsByAggregateType)
	return &continuum.EventStore{
		Reader: memoryeventreader.NewEventReader(events),
		Writer: memoryeventwriter.NewEventWriter(events),
	}
}
