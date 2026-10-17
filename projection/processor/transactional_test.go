package processor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	cpmemory "github.com/go-estoria/estoria/projection/checkpointstore/memory"
	"github.com/go-estoria/estoria/projection/processor"
)

var (
	errBegin    = errors.New("begin failed")
	errSave     = errors.New("transactional save failed")
	errCommit   = errors.New("commit failed")
	errRollback = errors.New("rollback failed")
	errHandler  = errors.New("handler failed")
)

func TestNew_TransactionalCheckpoints(t *testing.T) {
	t.Parallel()

	events, _ := newStores(t)

	handler := &collector{}
	id := projection.ID{Name: "orders", Version: 1}

	const (
		missingCapability = "transactional checkpoints require a checkpoint store that implements checkpointstore.TransactionalStore"
		cadenceRequired   = "transactional checkpoints with continue-on-handler-error require a checkpoint interval of 1"
	)

	for _, tt := range []struct {
		name    string
		make    func() (*processor.Processor, error)
		wantErr string
	}{
		{"rejects a store lacking the capability", func() (*processor.Processor, error) {
			return processor.New(events, plainStore{cpmemory.NewCheckpointStore()}, id, handler,
				processor.WithTransactionalCheckpoints())
		}, missingCapability},
		{"rejects continue-on-error with a cadence above one", func() (*processor.Processor, error) {
			return processor.New(events, cpmemory.NewCheckpointStore(), id, handler,
				processor.WithTransactionalCheckpoints(),
				processor.WithContinueOnHandlerError(true),
				processor.WithCheckpointEvery(2))
		}, cadenceRequired},
		{"rejects continue-on-error with a cadence above one regardless of option order", func() (*processor.Processor, error) {
			return processor.New(events, cpmemory.NewCheckpointStore(), id, handler,
				processor.WithCheckpointEvery(2),
				processor.WithContinueOnHandlerError(true),
				processor.WithTransactionalCheckpoints())
		}, cadenceRequired},
		{"accepts a transactional store", func() (*processor.Processor, error) {
			return processor.New(events, cpmemory.NewCheckpointStore(), id, handler,
				processor.WithTransactionalCheckpoints())
		}, ""},
		{"accepts continue-on-error at a cadence of one", func() (*processor.Processor, error) {
			return processor.New(events, cpmemory.NewCheckpointStore(), id, handler,
				processor.WithTransactionalCheckpoints(),
				processor.WithContinueOnHandlerError(true),
				processor.WithCheckpointEvery(1))
		}, ""},
		{"accepts a cadence above one without continue-on-error", func() (*processor.Processor, error) {
			return processor.New(events, cpmemory.NewCheckpointStore(), id, handler,
				processor.WithTransactionalCheckpoints(),
				processor.WithCheckpointEvery(5))
		}, ""},
		{"accepts a store lacking the capability without the option", func() (*processor.Processor, error) {
			return processor.New(events, plainStore{cpmemory.NewCheckpointStore()}, id, handler,
				processor.WithContinueOnHandlerError(true),
				processor.WithCheckpointEvery(5))
		}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := tt.make()
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("want error %q, got %v", tt.wantErr, err)
				}

				return
			}

			if err != nil || p == nil {
				t.Errorf("want a processor, got %v, %v", p, err)
			}
		})
	}
}

// TestProcessor_Transactional_WindowPerEventAtCadenceOne pins one transaction per event:
// the handler runs inside it, the save follows inside it, the commit ends it, and the
// idle touch at the head runs in a transaction of its own.
func TestProcessor_Transactional_WindowPerEventAtCadenceOne(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 3)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store}
	p := newProcessor(t, events, store, handler, processor.WithTransactionalCheckpoints())

	runToCaughtUp(t, p)

	store.assertCallsPrefix(t,
		"tx1:begin", "tx1:handle:1", "tx1:save:1", "tx1:commit",
		"tx2:begin", "tx2:handle:2", "tx2:save:2", "tx2:commit",
		"tx3:begin", "tx3:handle:3", "tx3:save:3", "tx3:commit",
		"tx4:begin", "tx4:save:3", "tx4:commit",
	)
	store.assertNoPlainSave(t)

	if got := loadPosition(t, inner); got != 3 {
		t.Errorf("want checkpointed position 3, got %d", got)
	}
}

// TestProcessor_Transactional_WindowPerCadence pins one transaction per window of n
// events, with a window cut short by the head committing rather than opening a second
// transaction.
func TestProcessor_Transactional_WindowPerCadence(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 7)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(3),
	)

	runUntilCalls(t, p, store, 19)

	store.assertCallsPrefix(t,
		"tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:handle:3", "tx1:save:3", "tx1:commit",
		"tx2:begin", "tx2:handle:4", "tx2:handle:5", "tx2:handle:6", "tx2:save:6", "tx2:commit",
		"tx3:begin", "tx3:handle:7", "tx3:save:7", "tx3:commit",
		"tx4:begin", "tx4:save:7", "tx4:commit",
	)
	store.assertNoPlainSave(t)
}

// TestProcessor_Transactional_WindowSpansBatchLimitedReads pins that a window is not
// bounded by a read: the transaction opened for the first batch stays open across the
// next read and commits once.
func TestProcessor_Transactional_WindowSpansBatchLimitedReads(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 4)

	reads := &readRecorder{inner: events}
	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store}
	p := newProcessor(t, reads, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithBatchSize(2),
		processor.WithCheckpointEvery(4),
	)

	runToCaughtUp(t, p)

	store.assertCallsPrefix(t,
		"tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:handle:3", "tx1:handle:4", "tx1:save:4", "tx1:commit",
		"tx2:begin", "tx2:save:4", "tx2:commit",
	)

	if got := len(reads.snapshot()); got < 3 {
		t.Errorf("want the window spread over at least two full reads and the head read, got %d reads", got)
	}
}

// TestProcessor_Transactional_HeadCommitsOpenWindow pins that reaching the head ends
// an open window instead of opening a second transaction beside it, and that
// CaughtUp certifies the position that commit made durable.
func TestProcessor_Transactional_HeadCommitsOpenWindow(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 3)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(10),
	)

	runUntilCalls(t, p, store, 9)

	store.assertCallsPrefix(t,
		"tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:handle:3", "tx1:save:3", "tx1:commit",
		"tx2:begin", "tx2:save:3", "tx2:commit",
	)

	if got := p.CaughtUpPosition(); got != 3 {
		t.Errorf("want CaughtUpPosition 3, got %d", got)
	}
}

// TestProcessor_Transactional_IdleTouchWithoutWindow pins that a processor with
// nothing to handle still saves its head position in a transaction of its own.
func TestProcessor_Transactional_IdleTouchWithoutWindow(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)

	store := newScriptedTxStore(inner)
	p := newProcessor(t, events, store, &txHandler{store: store}, processor.WithTransactionalCheckpoints())

	runUntilCalls(t, p, store, 6)

	store.assertCallsPrefix(t,
		"tx1:begin", "tx1:save:0", "tx1:commit",
		"tx2:begin", "tx2:save:0", "tx2:commit",
	)
}

// TestProcessor_Transactional_MemoryStoreHandsHandlerItsTransaction runs the option
// against the in-memory store: each handler call sees the transaction of its window.
func TestProcessor_Transactional_MemoryStoreHandsHandlerItsTransaction(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 3)

	handler := &memoryTxHandler{}
	p := newProcessor(t, events, checkpoints, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(2),
	)

	runToCaughtUp(t, p)

	seen := handler.snapshot()
	if len(seen) != 3 {
		t.Fatalf("want 3 handled events, got %d", len(seen))
	}

	for i, tx := range seen {
		if tx == nil {
			t.Fatalf("want event %d handled with the window's transaction on its context, got none", i+1)
		}
	}

	if seen[0] != seen[1] {
		t.Error("want events 1 and 2 handled in one window's transaction")
	}

	if seen[1] == seen[2] {
		t.Error("want event 3 handled in a new window's transaction")
	}

	if got := loadPosition(t, checkpoints); got != 3 {
		t.Errorf("want checkpointed position 3, got %d", got)
	}
}

func TestProcessor_Transactional_HandlerErrorInStopMode(t *testing.T) {
	t.Parallel()

	t.Run("rolls back the window and leaves the checkpoint untouched", func(t *testing.T) {
		t.Parallel()

		events, inner := newStores(t)
		appendEvents(t, events, 5)

		store := newScriptedTxStore(inner)
		handler := &txHandler{store: store, failAt: 2, failWith: errHandler}
		p := newProcessor(t, events, store, handler,
			processor.WithTransactionalCheckpoints(),
			processor.WithCheckpointEvery(3),
		)

		err := runToFailure(t, p)
		if !errors.Is(err, errHandler) {
			t.Errorf("want the handler error, got %v", err)
		}

		store.assertCalls(t, "tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:rollback")

		if _, err := inner.Load(t.Context(), testID()); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
			t.Errorf("want no checkpoint, got %v", err)
		}

		if got := p.Position(); got != 1 {
			t.Errorf("want the in-memory position 1 ahead of the durable checkpoint, got %d", got)
		}
	})

	t.Run("keeps the previous window's checkpoint", func(t *testing.T) {
		t.Parallel()

		events, inner := newStores(t)
		appendEvents(t, events, 3)

		store := newScriptedTxStore(inner)
		handler := &txHandler{store: store, failAt: 2, failWith: errHandler}
		p := newProcessor(t, events, store, handler, processor.WithTransactionalCheckpoints())

		if err := runToFailure(t, p); !errors.Is(err, errHandler) {
			t.Errorf("want the handler error, got %v", err)
		}

		store.assertCalls(t,
			"tx1:begin", "tx1:handle:1", "tx1:save:1", "tx1:commit",
			"tx2:begin", "tx2:handle:2", "tx2:rollback",
		)

		if got := loadPosition(t, inner); got != 1 {
			t.Errorf("want the checkpoint held at 1, got %d", got)
		}
	})

	t.Run("returns the handler error when the rollback fails too", func(t *testing.T) {
		t.Parallel()

		events, inner := newStores(t)
		appendEvents(t, events, 2)

		store := newScriptedTxStore(inner)
		store.rollbackErrs = []error{errRollback}
		handler := &txHandler{store: store, failAt: 1, failWith: errHandler}
		p := newProcessor(t, events, store, handler,
			processor.WithTransactionalCheckpoints(),
			processor.WithLogger(discardLogger{}),
		)

		err := runToFailure(t, p)
		if !errors.Is(err, errHandler) {
			t.Errorf("want the handler error, got %v", err)
		}

		store.assertCalls(t, "tx1:begin", "tx1:handle:1", "tx1:rollback")
	})
}

// TestProcessor_Transactional_HandlerErrorInContinueMode pins that a failed event's
// transaction is rolled back before the skipped position is saved in a fresh one.
func TestProcessor_Transactional_HandlerErrorInContinueMode(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 3)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store, failAt: 2, failWith: errHandler}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithContinueOnHandlerError(true),
		processor.WithLogger(discardLogger{}),
	)

	runToCaughtUp(t, p)

	store.assertCallsPrefix(t,
		"tx1:begin", "tx1:handle:1", "tx1:save:1", "tx1:commit",
		"tx2:begin", "tx2:handle:2", "tx2:rollback",
		"tx3:begin", "tx3:save:2", "tx3:commit",
		"tx4:begin", "tx4:handle:3", "tx4:save:3", "tx4:commit",
	)

	if got := loadPosition(t, inner); got != 3 {
		t.Errorf("want the checkpoint advanced past the failed event to 3, got %d", got)
	}
}

// TestProcessor_Transactional_RollbackFailureInContinueMode pins that a rollback that
// fails is terminal: no skip checkpoint is attempted and both failures are reported.
func TestProcessor_Transactional_RollbackFailureInContinueMode(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 3)

	store := newScriptedTxStore(inner)
	store.rollbackErrs = []error{errRollback}
	handler := &txHandler{store: store, failAt: 2, failWith: errHandler}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithContinueOnHandlerError(true),
		processor.WithLogger(discardLogger{}),
	)

	err := runToFailure(t, p)
	if !errors.Is(err, errHandler) || !errors.Is(err, errRollback) {
		t.Errorf("want the handler error with the rollback failure attached, got %v", err)
	}

	store.assertCalls(t,
		"tx1:begin", "tx1:handle:1", "tx1:save:1", "tx1:commit",
		"tx2:begin", "tx2:handle:2", "tx2:rollback",
	)

	if got := loadPosition(t, inner); got != 1 {
		t.Errorf("want the checkpoint held at 1, got %d", got)
	}
}

func TestProcessor_Transactional_SaveFailureRollsBackWindow(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 4)

	store := newScriptedTxStore(inner)
	store.saveErrs = []error{nil, errSave}
	handler := &txHandler{store: store}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(2),
	)

	if err := runToFailure(t, p); !errors.Is(err, errSave) {
		t.Errorf("want the save failure, got %v", err)
	}

	store.assertCalls(t,
		"tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:save:2", "tx1:commit",
		"tx2:begin", "tx2:handle:3", "tx2:handle:4", "tx2:save:4", "tx2:rollback",
	)

	if got := loadPosition(t, inner); got != 2 {
		t.Errorf("want the checkpoint held at 2, got %d", got)
	}
}

func TestProcessor_Transactional_BeginFailure(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		events int
	}{
		{"before the first event of a window", 2},
		{"at the idle touch", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, inner := newStores(t)
			if tt.events > 0 {
				appendEvents(t, events, tt.events)
			}

			store := newScriptedTxStore(inner)
			store.beginErrs = []error{errBegin}
			handler := &txHandler{store: store}
			p := newProcessor(t, events, store, handler, processor.WithTransactionalCheckpoints())

			if err := runToFailure(t, p); !errors.Is(err, errBegin) {
				t.Errorf("want the begin failure, got %v", err)
			}

			store.assertCalls(t, "begin:error")
			assertCaughtUpWithheld(t, p)
		})
	}
}

func TestProcessor_Transactional_ReadErrorMidWindowRollsBack(t *testing.T) {
	t.Parallel()

	_, inner := newStores(t)
	readErr := errors.New("read failed")
	reader := &scriptedReader{scripts: []iteratorScript{{
		events:  []*eventstore.Event{eventAt(1), eventAt(2)},
		readErr: readErr,
	}}}

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store}
	p := newProcessor(t, reader, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(5),
	)

	if err := runToFailure(t, p); !errors.Is(err, readErr) {
		t.Errorf("want the read failure, got %v", err)
	}

	store.assertCalls(t, "tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:rollback")
}

// TestProcessor_Transactional_CancellationRollsBackOnLiveContext pins that a window
// open at shutdown is rolled back, and on a context that cancellation has not reached.
func TestProcessor_Transactional_CancellationRollsBackOnLiveContext(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 3)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store, onHandle: func(position int64) {
		if position == 2 {
			cancel()
		}
	}}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(5),
	)

	done := make(chan error, 1)

	go func() {
		done <- p.Run(ctx)
	}()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want Run to return the context's error, got %v", err)
	}

	store.assertCalls(t, "tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:rollback")

	rollbackCtxErrs := store.rollbackContextErrors()
	if len(rollbackCtxErrs) != 1 || rollbackCtxErrs[0] != nil {
		t.Errorf("want one rollback on a context that is not done, got context errors %v", rollbackCtxErrs)
	}

	if _, err := inner.Load(t.Context(), testID()); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want no checkpoint, got %v", err)
	}
}

// TestProcessor_Transactional_CancellationDuringLastEventOfWindowDoesNotFlush pins that
// shutdown never flushes: a handler that cancels the run while handling the event that
// would complete its window gets a rollback, not a save and commit, even from a store
// that ignores the cancelled context.
func TestProcessor_Transactional_CancellationDuringLastEventOfWindowDoesNotFlush(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 2)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store, onHandle: func(position int64) {
		if position == 1 {
			cancel()
		}
	}}
	p := newProcessor(t, events, store, handler, processor.WithTransactionalCheckpoints())

	done := make(chan error, 1)

	go func() {
		done <- p.Run(ctx)
	}()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want Run to return the context's error, got %v", err)
	}

	store.assertCalls(t, "tx1:begin", "tx1:handle:1", "tx1:rollback")

	if rollbackCtxErrs := store.rollbackContextErrors(); len(rollbackCtxErrs) != 1 || rollbackCtxErrs[0] != nil {
		t.Errorf("want one rollback on a live context, got context errors %v", rollbackCtxErrs)
	}

	if _, err := inner.Load(t.Context(), testID()); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want no checkpoint, got %v", err)
	}
}

// TestProcessor_Transactional_PanicRollsBackDuringUnwinding pins that the cleanup
// survives a panicking handler.
func TestProcessor_Transactional_PanicRollsBackDuringUnwinding(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 3)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store, onHandle: func(position int64) {
		if position == 2 {
			panic("handler panicked")
		}
	}}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(5),
	)

	var recovered any

	func() {
		defer func() { recovered = recover() }()

		_ = p.Run(t.Context())
	}()

	if recovered != "handler panicked" {
		t.Errorf("want the handler's panic to propagate, got %v", recovered)
	}

	store.assertCalls(t, "tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:rollback")

	if rollbackCtxErrs := store.rollbackContextErrors(); len(rollbackCtxErrs) != 1 || rollbackCtxErrs[0] != nil {
		t.Errorf("want one rollback on a live context, got context errors %v", rollbackCtxErrs)
	}
}

// TestProcessor_Transactional_CommitFailureBeforeCatchUp pins that a commit error stops
// the processor without opening another transaction or signaling caught-up.
func TestProcessor_Transactional_CommitFailureBeforeCatchUp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		every int
		want  []string
	}{
		{"at the head", 10, []string{
			"tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:handle:3", "tx1:save:3", "tx1:commit",
		}},
		{"before the head", 1, []string{
			"tx1:begin", "tx1:handle:1", "tx1:save:1", "tx1:commit",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, inner := newStores(t)
			appendEvents(t, events, 3)

			store := newScriptedTxStore(inner)
			store.commitErrs = []error{errCommit}
			handler := &txHandler{store: store}
			p := newProcessor(t, events, store, handler,
				processor.WithTransactionalCheckpoints(),
				processor.WithCheckpointEvery(tt.every),
			)

			if err := runToFailure(t, p); !errors.Is(err, errCommit) {
				t.Errorf("want the commit failure, got %v", err)
			}

			store.assertCalls(t, tt.want...)
			assertCaughtUpWithheld(t, p)
		})
	}
}

// TestProcessor_Transactional_CommitFailureAfterCatchUp pins that a commit error after
// the first catch-up stops the processor without reopening the CaughtUp signal or
// changing the position it certified.
func TestProcessor_Transactional_CommitFailureAfterCatchUp(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 2)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store}
	p := newProcessor(t, events, store, handler, processor.WithTransactionalCheckpoints())

	_, done := start(t, p)
	waitCaughtUp(t, p)

	if got := p.CaughtUpPosition(); got != 2 {
		t.Fatalf("want CaughtUpPosition 2, got %d", got)
	}

	store.failFutureCommits(errCommit)
	appendEvents(t, events, 2)

	if err := waitDone(t, done); !errors.Is(err, errCommit) {
		t.Errorf("want the commit failure, got %v", err)
	}

	select {
	case <-p.CaughtUp():
	default:
		t.Error("want CaughtUp to remain closed")
	}

	if got := p.CaughtUpPosition(); got != 2 {
		t.Errorf("want CaughtUpPosition unchanged at 2, got %d", got)
	}
}

// TestProcessor_Transactional_WindowClosedByHeadAcrossBatches pins a window that spans
// batch-limited reads and is ended by reaching the head rather than by the cadence: one
// transaction, one save at the final position, one commit, and CaughtUp only after it.
func TestProcessor_Transactional_WindowClosedByHeadAcrossBatches(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 3)

	store := newScriptedTxStore(inner)
	handler := &txHandler{store: store}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithBatchSize(1),
		processor.WithCheckpointEvery(10),
	)

	cancel, done := start(t, p)
	waitCaughtUp(t, p)

	got := store.calls()
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want Run to return the context's error on cancellation, got %v", err)
	}

	want := []string{
		"tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:handle:3", "tx1:save:3", "tx1:commit",
	}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("want calls beginning\n  %v\ngot\n  %v", want, got)
	}
}

// TestProcessor_Transactional_CommitPersistedButReportedFailed pins recovery from a
// commit whose outcome the processor cannot know: a fresh processor reloads the
// durable checkpoint and resumes after the window.
func TestProcessor_Transactional_CommitPersistedButReportedFailed(t *testing.T) {
	t.Parallel()

	events, inner := newStores(t)
	appendEvents(t, events, 4)

	store := newScriptedTxStore(inner)
	store.commitErrs = []error{errCommit}
	store.commitPersistsOnError = true
	handler := &txHandler{store: store}
	p := newProcessor(t, events, store, handler,
		processor.WithTransactionalCheckpoints(),
		processor.WithCheckpointEvery(2),
	)

	if err := runToFailure(t, p); !errors.Is(err, errCommit) {
		t.Errorf("want the commit failure, got %v", err)
	}

	store.assertCalls(t, "tx1:begin", "tx1:handle:1", "tx1:handle:2", "tx1:save:2", "tx1:commit")

	resumed := &collector{}
	p2 := newProcessor(t, events, inner, resumed)

	_, _ = start(t, p2)
	waitCaughtUp(t, p2)

	assertPositions(t, resumed.snapshot(), 3, 4)
}

// runToCaughtUp runs p until it has caught up, then stops it and waits for Run to
// return, so the call log can be read without racing the idle touches that follow.
func runToCaughtUp(t *testing.T, p *processor.Processor) {
	t.Helper()

	cancel, done := start(t, p)
	waitCaughtUp(t, p)
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want Run to return the context's error on cancellation, got %v", err)
	}
}

// runUntilCalls runs p until its store has logged n calls, then stops it and waits for
// Run to return. It is for runs that idle at the head, where waiting past the first
// idle touch needs more than CaughtUp.
func runUntilCalls(t *testing.T, p *processor.Processor, store *scriptedTxStore, n int) {
	t.Helper()

	cancel, done := start(t, p)
	waitFor(t, func() bool { return len(store.calls()) >= n })
	waitCaughtUp(t, p)
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want Run to return the context's error on cancellation, got %v", err)
	}
}

// runToFailure runs p until Run returns on its own and returns its error.
func runToFailure(t *testing.T, p *processor.Processor) error {
	t.Helper()

	_, done := start(t, p)

	err := waitDone(t, done)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("want Run to fail, got %v", err)
	}

	return err
}

// plainStore hides everything but checkpointstore.Store, so the wrapped store's
// transactional capability is not discoverable.
type plainStore struct {
	checkpointstore.Store
}

type scriptedTxKey struct{}

// scriptedTxStore is a transactional checkpoint store over a committed store. It logs
// every transaction call and every handler call in order, and takes a scripted result
// for the nth call of each operation: a missing or nil entry means success.
type scriptedTxStore struct {
	checkpointstore.Store

	beginErrs    []error
	saveErrs     []error
	commitErrs   []error
	rollbackErrs []error

	// commitPersistsOnError applies a transaction's save to the committed store
	// even when its scripted Commit result is an error.
	commitPersistsOnError bool

	// commitFailure, once set, fails every later Commit regardless of the script.
	commitFailure error

	mu              sync.Mutex
	log             []string
	begins          int
	saves           int
	commits         int
	rollbacks       int
	rollbackCtxErrs []error
}

func newScriptedTxStore(committed checkpointstore.Store) *scriptedTxStore {
	return &scriptedTxStore{Store: committed}
}

func (s *scriptedTxStore) record(entry string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log = append(s.log, entry)
}

func (s *scriptedTxStore) Save(ctx context.Context, id projection.ID, position int64) error {
	s.record(fmt.Sprintf("plain-save:%d", position))

	return s.Store.Save(ctx, id, position)
}

func (s *scriptedTxStore) Begin(ctx context.Context) (checkpointstore.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := scripted(s.beginErrs, s.begins)
	s.begins++

	if err != nil {
		s.log = append(s.log, "begin:error")

		return nil, err
	}

	tx := &scriptedTx{store: s, number: s.begins, ctx: context.WithValue(ctx, scriptedTxKey{}, s.begins)}
	s.log = append(s.log, tx.entry("begin"))

	return tx, nil
}

func scripted(results []error, call int) error {
	if call < len(results) {
		return results[call]
	}

	return nil
}

func (s *scriptedTxStore) failFutureCommits(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.commitFailure = err
}

func (s *scriptedTxStore) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.log)
}

func (s *scriptedTxStore) rollbackContextErrors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.rollbackCtxErrs)
}

// assertCalls asserts the complete call log, for runs that have ended on their own.
func (s *scriptedTxStore) assertCalls(t *testing.T, want ...string) {
	t.Helper()

	if got := s.calls(); !slices.Equal(got, want) {
		t.Errorf("want calls\n  %v\ngot\n  %v", want, got)
	}
}

// assertCallsPrefix asserts the start of the call log, for runs that were stopped
// while idling at the head and so end in a variable number of idle touches.
func (s *scriptedTxStore) assertCallsPrefix(t *testing.T, want ...string) {
	t.Helper()

	got := s.calls()
	if len(got) < len(want) {
		t.Errorf("want calls beginning\n  %v\ngot only\n  %v", want, got)
		return
	}

	if !slices.Equal(got[:len(want)], want) {
		t.Errorf("want calls beginning\n  %v\ngot\n  %v", want, got[:len(want)])
	}
}

// assertNoPlainSave asserts that no checkpoint was saved outside a transaction.
func (s *scriptedTxStore) assertNoPlainSave(t *testing.T) {
	t.Helper()

	for _, call := range s.calls() {
		if strings.HasPrefix(call, "plain-save") {
			t.Errorf("want every checkpoint saved in a transaction, got call %q", call)
		}
	}
}

type pendingSave struct {
	id       projection.ID
	position int64
}

type scriptedTx struct {
	store  *scriptedTxStore
	number int
	ctx    context.Context //nolint:containedctx

	pending []pendingSave
	ended   bool
}

func (tx *scriptedTx) entry(op string) string {
	return fmt.Sprintf("tx%d:%s", tx.number, op)
}

func (tx *scriptedTx) Context() context.Context {
	return tx.ctx
}

func (tx *scriptedTx) Save(_ context.Context, id projection.ID, position int64) error {
	s := tx.store

	s.mu.Lock()
	defer s.mu.Unlock()

	err := scripted(s.saveErrs, s.saves)
	s.saves++
	s.log = append(s.log, tx.entry(fmt.Sprintf("save:%d", position)))

	if err != nil {
		return err
	}

	tx.pending = append(tx.pending, pendingSave{id: id, position: position})

	return nil
}

func (tx *scriptedTx) Commit(ctx context.Context) error {
	s := tx.store

	s.mu.Lock()
	defer s.mu.Unlock()

	err := scripted(s.commitErrs, s.commits)
	if s.commitFailure != nil {
		err = s.commitFailure
	}

	s.commits++
	s.log = append(s.log, tx.entry("commit"))

	if tx.ended {
		return errors.New("transaction has ended")
	}

	tx.ended = true

	if err != nil && !s.commitPersistsOnError {
		return err
	}

	for _, save := range tx.pending {
		if saveErr := s.Store.Save(ctx, save.id, save.position); saveErr != nil {
			return saveErr
		}
	}

	return err
}

func (tx *scriptedTx) Rollback(ctx context.Context) error {
	s := tx.store

	s.mu.Lock()
	defer s.mu.Unlock()

	err := scripted(s.rollbackErrs, s.rollbacks)
	s.rollbacks++
	s.log = append(s.log, tx.entry("rollback"))
	s.rollbackCtxErrs = append(s.rollbackCtxErrs, ctx.Err())

	if tx.ended {
		return nil
	}

	tx.ended = true
	tx.pending = nil

	return err
}

// txHandler logs each event it handles with the number of the scripted transaction
// found on its context, or 0 if the context carries none.
type txHandler struct {
	store    *scriptedTxStore
	failAt   int64
	failWith error
	onHandle func(position int64)
}

func (h *txHandler) Handle(ctx context.Context, event *eventstore.Event) error {
	position := *event.GlobalPosition

	number, _ := ctx.Value(scriptedTxKey{}).(int)
	h.store.record(fmt.Sprintf("tx%d:handle:%d", number, position))

	if h.onHandle != nil {
		h.onHandle(position)
	}

	if h.failAt != 0 && position == h.failAt {
		return h.failWith
	}

	return nil
}

// memoryTxHandler records the in-memory transaction found on each handled event's
// context, nil where the context carries none.
type memoryTxHandler struct {
	mu   sync.Mutex
	seen []*cpmemory.Transaction
}

func (h *memoryTxHandler) Handle(ctx context.Context, _ *eventstore.Event) error {
	tx, _ := cpmemory.FromContext(ctx)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.seen = append(h.seen, tx)

	return nil
}

func (h *memoryTxHandler) snapshot() []*cpmemory.Transaction {
	h.mu.Lock()
	defer h.mu.Unlock()

	return slices.Clone(h.seen)
}
