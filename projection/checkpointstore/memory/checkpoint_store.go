// Package memory provides an in-memory checkpoint store.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
)

// A CheckpointStore is an in-memory checkpoint store. It should not be used in
// production applications.
type CheckpointStore struct {
	checkpoints map[projection.ID]checkpointstore.Checkpoint
	mu          sync.RWMutex
}

// NewCheckpointStore creates a new in-memory checkpoint store.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{
		checkpoints: map[projection.ID]checkpointstore.Checkpoint{},
	}
}

// Load returns the projection's checkpoint.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (s *CheckpointStore) Load(_ context.Context, id projection.ID) (checkpointstore.Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	checkpoint, ok := s.checkpoints[id]
	if !ok {
		return checkpointstore.Checkpoint{}, checkpointstore.ErrCheckpointNotFound
	}

	return checkpoint, nil
}

// Save records position as the projection's checkpoint.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (s *CheckpointStore) Save(_ context.Context, id projection.ID, position int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.checkpoints[id] = checkpointstore.Checkpoint{
		ProjectionID: id,
		Position:     position,
		UpdatedAt:    time.Now().UTC(),
	}

	return nil
}

// Delete removes the projection's checkpoint.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (s *CheckpointStore) Delete(_ context.Context, id projection.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.checkpoints[id]; !ok {
		return checkpointstore.ErrCheckpointNotFound
	}

	delete(s.checkpoints, id)

	return nil
}

var _ checkpointstore.Store = (*CheckpointStore)(nil)
