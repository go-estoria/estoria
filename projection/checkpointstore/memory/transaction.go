package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
)

// ErrTransactionEnded is returned by Save and Commit on a transaction that has
// been committed or rolled back.
var ErrTransactionEnded = errors.New("transaction has ended")

type transactionKey struct{}

// A Transaction is an open transaction on a CheckpointStore. It buffers saves
// and applies them to the store's committed checkpoints on Commit.
type Transaction struct {
	store *CheckpointStore
	ctx   context.Context //nolint:containedctx

	mu    sync.Mutex
	saves []checkpointstore.Checkpoint
	ended bool
}

// FromContext returns the transaction that a CheckpointStore's Begin placed on
// ctx, or false if ctx carries none.
func FromContext(ctx context.Context) (*Transaction, bool) {
	tx, ok := ctx.Value(transactionKey{}).(*Transaction)

	return tx, ok
}

// Context returns the context derived from the one passed to Begin that
// carries the transaction, for retrieval with FromContext.
func (t *Transaction) Context() context.Context {
	return t.ctx
}

// Save buffers position as the projection's checkpoint, to be applied on
// Commit. It returns ErrTransactionEnded once the transaction has ended.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (t *Transaction) Save(_ context.Context, id projection.ID, position int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ended {
		return ErrTransactionEnded
	}

	t.saves = append(t.saves, checkpointstore.Checkpoint{
		ProjectionID: id,
		Position:     position,
		UpdatedAt:    time.Now().UTC(),
	})

	return nil
}

// Commit applies the buffered saves to the store and ends the transaction. It
// returns ErrTransactionEnded once the transaction has ended.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (t *Transaction) Commit(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ended {
		return ErrTransactionEnded
	}

	t.store.mu.Lock()
	for _, checkpoint := range t.saves {
		t.store.checkpoints[checkpoint.ProjectionID] = checkpoint
	}
	t.store.mu.Unlock()

	t.end()

	return nil
}

// Rollback discards the buffered saves and ends the transaction. It returns nil
// if the transaction has already ended.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (t *Transaction) Rollback(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.ended {
		t.end()
	}

	return nil
}

// end marks the transaction ended and lets the next Begin proceed. The caller
// holds t.mu and has checked that the transaction has not ended.
func (t *Transaction) end() {
	t.ended = true
	t.saves = nil
	<-t.store.open
}

var _ checkpointstore.Transaction = (*Transaction)(nil)
