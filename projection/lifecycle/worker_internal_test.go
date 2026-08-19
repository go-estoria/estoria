package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
)

// untouchableReader counts reads and must not receive any.
type untouchableReader struct{ calls int }

func (r *untouchableReader) ReadAll(context.Context, eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	r.calls++
	return nil, errors.New("the reader must not be touched")
}

type nopSetter struct{}

func (nopSetter) ApplyCutover(context.Context, Cutover) error { return nil }

func (nopSetter) AppliedCutover(context.Context, string) (Cutover, error) {
	return Cutover{}, ErrNoLiveVersion
}

// TestDrainCancellationPrecedesTheRead pins the drain's entry check: a drain
// entered with a canceled context issues no read at all.
func TestDrainCancellationPrecedesTheRead(t *testing.T) {
	t.Parallel()

	reader := &untouchableReader{}

	worker, err := NewWorker(reader, WithCutoverSetter(nopSetter{}))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	position, err := worker.drain(ctx, map[string]cutoverFold{}, 7, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if position != 7 {
		t.Errorf("want the position unmoved at 7, got %d", position)
	}

	if reader.calls != 0 {
		t.Errorf("want no read issued from a canceled drain, got %d", reader.calls)
	}
}
