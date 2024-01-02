package eventstore

import (
	"context"
	"fmt"

	"github.com/jefflinse/continuum"
)

// An EventReader is anything that can read events.
type EventReader interface {
	ReadEvents(ctx context.Context, aggregateType string, aggregateID continuum.Identifier, versions continuum.VersionSpec) ([]*continuum.BasicEvent, error)
}

// An EventWriter is anything that can write events.
type EventWriter interface {
	WriteEvents(ctx context.Context, events []*continuum.BasicEvent) error
}

// An EventStore is anything that can load and save events.
type EventStore struct {
	Reader EventReader
	Writer EventWriter
}

// New creates a new EventStore.
func New(reader EventReader, writer EventWriter) *EventStore {
	return &EventStore{
		Reader: reader,
		Writer: writer,
	}
}

// LoadEvents loads events for the given aggregate type and ID.
func (s EventStore) LoadEvents(ctx context.Context, aggregateType string, aggregateID continuum.Identifier, versions continuum.VersionSpec) ([]*continuum.BasicEvent, error) {
	if s.Reader == nil {
		return nil, fmt.Errorf("no event reader configured")
	}

	if err := versions.Validate(); err != nil {
		return nil, fmt.Errorf("invalid version spec: %w", err)
	}

	events, err := s.Reader.ReadEvents(ctx, aggregateType, aggregateID, versions)
	if err != nil {
		return nil, fmt.Errorf("reading events: %w", err)
	}

	return events, nil
}

// SaveEvents saves the given events.
func (s EventStore) SaveEvents(ctx context.Context, events []*continuum.BasicEvent) error {
	if s.Writer == nil {
		return fmt.Errorf("no event writer configured")
	}

	err := s.Writer.WriteEvents(ctx, events)
	if err != nil {
		return fmt.Errorf("writing events: %w", err)
	}

	return nil
}
