package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/go-estoria/estoria/projection/checkpointstore/memory"
)

var testID = projection.ID{Name: "memorytest", Version: 1}

func TestTransaction_ContextCarriesTransaction(t *testing.T) {
	t.Parallel()

	store := memory.NewCheckpointStore()

	tx, err := store.Begin(t.Context())
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })

	got, ok := memory.FromContext(tx.Context())
	if !ok {
		t.Fatal("want the transaction on its own context")
	}

	if checkpointstore.Transaction(got) != tx {
		t.Error("want the context's transaction to be the one Begin returned")
	}

	if _, ok := memory.FromContext(t.Context()); ok {
		t.Error("want no transaction on a context that was not derived from Begin")
	}
}

func TestTransaction_ContextDerivesFromBeginContext(t *testing.T) {
	t.Parallel()

	type key struct{}

	store := memory.NewCheckpointStore()
	ctx := context.WithValue(t.Context(), key{}, "value")

	tx, err := store.Begin(ctx)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })

	if got := tx.Context().Value(key{}); got != "value" {
		t.Errorf("want the Begin context's values preserved, got %v", got)
	}
}

func TestTransaction_EndedTransactionRejectsSaveAndCommit(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		end  func(checkpointstore.Transaction) error
	}{
		{"after commit", func(tx checkpointstore.Transaction) error { return tx.Commit(t.Context()) }},
		{"after rollback", func(tx checkpointstore.Transaction) error { return tx.Rollback(t.Context()) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := memory.NewCheckpointStore()

			tx, err := store.Begin(t.Context())
			if err != nil {
				t.Fatalf("beginning: %v", err)
			}

			if err := tt.end(tx); err != nil {
				t.Fatalf("ending: %v", err)
			}

			if err := tx.Save(t.Context(), testID, 1); !errors.Is(err, memory.ErrTransactionEnded) {
				t.Errorf("want ErrTransactionEnded from Save, got %v", err)
			}

			if err := tx.Commit(t.Context()); !errors.Is(err, memory.ErrTransactionEnded) {
				t.Errorf("want ErrTransactionEnded from Commit, got %v", err)
			}

			if err := tx.Rollback(t.Context()); err != nil {
				t.Errorf("want nil from Rollback, got %v", err)
			}
		})
	}
}

func TestTransaction_BufferedSavesApplyInOrderOnCommit(t *testing.T) {
	t.Parallel()

	store := memory.NewCheckpointStore()

	tx, err := store.Begin(t.Context())
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}

	for _, position := range []int64{5, 2} {
		if err := tx.Save(t.Context(), testID, position); err != nil {
			t.Fatalf("saving %d: %v", position, err)
		}
	}

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("committing: %v", err)
	}

	checkpoint, err := store.Load(t.Context(), testID)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	if checkpoint.Position != 2 {
		t.Errorf("want the last buffered save's position 2, got %d", checkpoint.Position)
	}
}

func TestBegin_WaitsForOpenTransaction(t *testing.T) {
	t.Parallel()

	store := memory.NewCheckpointStore()

	first, err := store.Begin(t.Context())
	if err != nil {
		t.Fatalf("beginning first: %v", err)
	}

	type result struct {
		tx  checkpointstore.Transaction
		err error
	}

	second := make(chan result, 1)

	go func() {
		tx, err := store.Begin(t.Context())
		second <- result{tx, err}
	}()

	select {
	case <-second:
		t.Fatal("want Begin to wait while another transaction is open")
	case <-time.After(50 * time.Millisecond):
	}

	if err := first.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back first: %v", err)
	}

	select {
	case got := <-second:
		if got.err != nil {
			t.Fatalf("beginning second: %v", got.err)
		}

		if err := got.tx.Rollback(t.Context()); err != nil {
			t.Errorf("rolling back second: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Begin to proceed after the first transaction ended")
	}
}

func TestBegin_ReturnsContextErrorWhileWaiting(t *testing.T) {
	t.Parallel()

	store := memory.NewCheckpointStore()

	first, err := store.Begin(t.Context())
	if err != nil {
		t.Fatalf("beginning first: %v", err)
	}
	t.Cleanup(func() { _ = first.Rollback(context.WithoutCancel(t.Context())) })

	ctx, cancel := context.WithCancel(t.Context())

	errs := make(chan error, 1)

	go func() {
		_, err := store.Begin(ctx)
		errs <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a canceled Begin to return")
	}
}

func TestBegin_RejectsDoneContext(t *testing.T) {
	t.Parallel()

	store := memory.NewCheckpointStore()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := store.Begin(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled even with no transaction open, got %v", err)
	}
}

func TestTransaction_DoesNotBlockPlainAccess(t *testing.T) {
	t.Parallel()

	store := memory.NewCheckpointStore()

	tx, err := store.Begin(t.Context())
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })

	done := make(chan error, 1)

	go func() {
		if err := store.Save(t.Context(), testID, 1); err != nil {
			done <- err
			return
		}

		_, err := store.Load(t.Context(), testID)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("plain access during an open transaction: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plain Save and Load blocked while a transaction was open")
	}
}
