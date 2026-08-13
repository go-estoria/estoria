package rebuild

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/go-estoria/estoria/typeid"
)

// A Rebuild is a handle driving one rebuild aggregate. Run blocks while the
// build's processor runs; Promote, Rollback, Abandon, and Retire are command
// methods, safe to call from other goroutines. After a version-mismatch error
// — a competing orchestrator recorded a transition first — discard the handle
// and reload the rebuild via Resume to observe what won.
type Rebuild struct {
	orchestrator *Orchestrator

	mu            sync.Mutex
	aggregate     *aggregatestore.Aggregate[State]
	stopProcessor context.CancelFunc

	// processorExited is closed when Run's processor goroutine has fully
	// exited; commands that clean up after stopping the processor wait on it
	// so cleanup cannot race a final checkpoint save.
	processorExited chan struct{}

	// stopped records that a command (Abandon, Rollback) deliberately stopped
	// the processor, so Run reports nil rather than the cancellation.
	stopped bool
}

// ID returns the rebuild aggregate's typed ID; its UUID is what Resume takes.
func (r *Rebuild) ID() typeid.ID {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.aggregate.ID()
}

// State returns a snapshot of the rebuild's folded state.
func (r *Rebuild) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.aggregate.State()
}

// Checkpoint returns the checkpoint of the version being built. Its recency
// is the build's liveness signal: the processor touches the checkpoint every
// poll cycle, so a checkpoint much older than the poll interval means the
// processor is not running.
func (r *Rebuild) Checkpoint(ctx context.Context) (checkpointstore.Checkpoint, error) {
	return r.orchestrator.config.Checkpoints.Load(ctx, r.State().Next)
}

// Run drives the build: it records the build starting (or resuming, when the
// rebuild was already building), runs a processor for the next version,
// records catch-up when the build first drains to the head, promotes if the
// orchestrator auto-promotes, and keeps the processor tailing until ctx is
// canceled. It returns nil after an Abandon stops the processor, and the
// context's error on cancellation.
func (r *Rebuild) Run(ctx context.Context) error {
	r.mu.Lock()

	// Refresh before deciding: entering at a caught-up or promoted phase
	// appends nothing, so a stale handle would otherwise start a processor
	// for a rebuild that was since rolled back, abandoned, or retired — and
	// a tailing processor appends nothing that would ever surface the
	// conflict.
	if err := r.orchestrator.config.Rebuilds.Hydrate(ctx, r.aggregate, nil); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("refreshing rebuild state: %w", err)
	}

	state := r.aggregate.State()

	var transition estoria.DomainEvent[State]

	switch state.Phase {
	case PhaseCreated:
		transition = BuildStarted{}
	case PhaseBuilding:
		position := int64(0)

		checkpoint, err := r.orchestrator.config.Checkpoints.Load(ctx, state.Next)
		if err == nil {
			position = checkpoint.Position
		} else if !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
			r.mu.Unlock()
			return fmt.Errorf("loading checkpoint: %w", err)
		}

		transition = BuildResumed{FromPosition: position}
	case PhaseCaughtUp, PhasePromoted:
		// No transition to record; run the processor so the version resumes
		// tailing the event sequence.
	case PhaseRolledBack, PhaseAbandoned, PhaseRetired:
		r.mu.Unlock()
		return fmt.Errorf("rebuild is %s; nothing to run", state.Phase)
	}

	// Append-then-act: the intent is recorded before the processor starts. A
	// crash between the two is exactly what resume reconciliation handles.
	if transition != nil {
		if err := r.appendLocked(ctx, transition); err != nil {
			r.mu.Unlock()
			return err
		}
	}

	handler, err := r.orchestrator.config.Handler(state.Next)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("creating handler for %s: %w", state.Next, err)
	}

	proc, err := processor.New(
		r.orchestrator.config.Events,
		r.orchestrator.config.Checkpoints,
		state.Next,
		handler,
		r.orchestrator.processorOptions...,
	)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("creating processor: %w", err)
	}

	processorCtx, stop := context.WithCancel(ctx)
	defer stop()

	done := make(chan error, 1)
	exited := make(chan struct{})

	r.stopProcessor = stop
	r.processorExited = exited
	catchingUp := state.Phase == PhaseCreated || state.Phase == PhaseBuilding
	r.mu.Unlock()

	started := time.Now()

	go func() {
		done <- proc.Run(processorCtx)
		close(exited)
	}()

	if catchingUp {
		if keepTailing, err := r.runToCaughtUp(ctx, proc, stop, done, started); !keepTailing {
			return err
		}
	}

	return r.processorExit(<-done)
}

// runToCaughtUp waits for the build's first drain to reach the head, records
// the CaughtUp transition, and promotes if the orchestrator auto-promotes. It
// reports whether Run should keep tailing; when it reports false, its error
// is Run's result.
func (r *Rebuild) runToCaughtUp(ctx context.Context, proc *processor.Processor, stop context.CancelFunc, done <-chan error, started time.Time) (bool, error) {
	select {
	case <-ctx.Done():
		<-done
		return false, ctx.Err()
	case err := <-done:
		return false, r.processorExit(err)
	case <-proc.CaughtUp():
	}

	r.mu.Lock()

	// Recheck under the lock: a same-handle Abandon between the caught-up
	// signal and this append shares the aggregate, so it would not surface
	// as a version conflict — CaughtUp would land cleanly on top of the
	// Abandoned event.
	if r.stopped {
		r.mu.Unlock()
		<-done

		return false, nil
	}

	err := r.appendLocked(ctx, CaughtUp{
		Position: proc.Position(),
		Duration: time.Since(started),
		At:       time.Now(),
	})
	r.mu.Unlock()

	if err != nil {
		stop()
		<-done

		// An Abandon can win the race against the catch-up transition; the
		// abandonment is recorded, so the lost append is not an error.
		if r.isStopped() {
			return false, nil
		}

		return false, err
	}

	if r.orchestrator.autoPromote {
		return r.promoteAfterCatchUp(ctx, stop, done)
	}

	return true, nil
}

// promoteAfterCatchUp auto-promotes, mapping the outcome to runToCaughtUp's
// contract: it reports whether Run should keep tailing.
func (r *Rebuild) promoteAfterCatchUp(ctx context.Context, stop context.CancelFunc, done <-chan error) (bool, error) {
	err := r.Promote(ctx)
	if err == nil {
		return true, nil
	}

	// A hook failure after the promotion was recorded marks a stale cache,
	// not a failed cutover: the version is live and must keep tailing.
	var hookErr CutoverHookError
	if errors.As(err, &hookErr) {
		r.orchestrator.log.Error("cutover hook failed after promotion",
			"rebuild_id", r.ID(), "live", hookErr.Live, "error", hookErr.Err)

		return true, nil
	}

	// The promotion can be durable even when the save could not observe it;
	// the hooks have run and the version is live, so keep tailing. Only this
	// handle is stale.
	if errors.Is(err, aggregatestore.ErrEventsAppended) {
		r.orchestrator.log.Error("promotion recorded, but the rebuild handle is stale",
			"rebuild_id", r.ID(), "error", err)

		return true, nil
	}

	stop()
	<-done

	// An Abandon can win the race against auto-promotion; the abandonment is
	// recorded, so the refused promotion is not an error.
	if r.isStopped() {
		return false, nil
	}

	return false, err
}

// Promote cuts reads over to the next version. It records Promoted first —
// the event is the flip — and then runs the cutover hooks with the now-live
// version; a hook error does not undo the promotion, it reports a cache that
// still needs the flip applied.
func (r *Rebuild) Promote(ctx context.Context) error {
	r.mu.Lock()

	state := r.aggregate.State()
	if state.Phase != PhaseCaughtUp {
		r.mu.Unlock()
		return fmt.Errorf("cannot promote a rebuild that is %s", state.Phase)
	}

	appendErr := r.appendLocked(ctx, Promoted{
		Previous: state.Previous,
		Next:     state.Next,
		At:       time.Now(),
	})

	r.mu.Unlock()

	// An error carrying ErrEventsAppended means the event is durable and the
	// aggregate could not observe it: the flip happened, so the hooks must
	// still run — only this handle is stale.
	if appendErr != nil && !errors.Is(appendErr, aggregatestore.ErrEventsAppended) {
		return appendErr
	}

	cutoverErr := r.orchestrator.cutover(ctx, state.Next)

	if appendErr != nil {
		return errors.Join(staleHandleError("promotion", appendErr), cutoverErr)
	}

	return cutoverErr
}

// Rollback reverts reads to the previous version. It records RolledBack first
// and then runs the cutover hooks with the reverted-to version. The rebuild
// is terminal afterwards — its processor is stopped, and a subsequent attempt
// is a new rebuild. The rolled-back version's storage is deliberately left in
// place for inspection; Begin cleans it up when the version number is reused.
func (r *Rebuild) Rollback(ctx context.Context) error {
	r.mu.Lock()

	state := r.aggregate.State()

	switch {
	case state.Phase != PhasePromoted:
		r.mu.Unlock()
		return fmt.Errorf("cannot roll back a rebuild that is %s", state.Phase)
	case state.Previous.Version == 0:
		r.mu.Unlock()
		return errors.New("rebuild has no previous version to roll back to")
	}

	appendErr := r.appendLocked(ctx, RolledBack{RevertedTo: state.Previous})
	if appendErr != nil && !errors.Is(appendErr, aggregatestore.ErrEventsAppended) {
		r.mu.Unlock()
		return appendErr
	}

	r.stopped = true
	r.mu.Unlock()

	cutoverErr := r.orchestrator.cutover(ctx, state.Previous)
	stopErr := r.awaitProcessorStop(ctx)

	if appendErr != nil {
		return errors.Join(staleHandleError("rollback", appendErr), cutoverErr, stopErr)
	}

	return errors.Join(cutoverErr, stopErr)
}

// awaitProcessorStop cancels the processor, if one is running, and waits for
// its goroutine to fully exit.
func (r *Rebuild) awaitProcessorStop(ctx context.Context) error {
	r.mu.Lock()
	stop := r.stopProcessor
	exited := r.processorExited
	r.mu.Unlock()

	if stop == nil {
		return nil
	}

	stop()

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Abandon gives up on the rebuild before promotion: it records Abandoned,
// stops the processor, and then cleans up the next version — tearing down its
// storage when the handler implements projection.Teardowner, and deleting its
// checkpoint. Cleanup errors are returned, but the abandonment is already
// recorded. Cleanup runs only when this handle ran the build's processor:
// abandoning through a handle that did not leaves the residue in place — a
// builder elsewhere may still be running, and Begin cleans residue up when
// the version number is reused.
func (r *Rebuild) Abandon(ctx context.Context, cause string) error {
	r.mu.Lock()

	state := r.aggregate.State()

	switch state.Phase {
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
	case PhasePromoted, PhaseRolledBack, PhaseAbandoned, PhaseRetired:
		r.mu.Unlock()
		return fmt.Errorf("cannot abandon a rebuild that is %s", state.Phase)
	}

	appendErr := r.appendLocked(ctx, Abandoned{Cause: cause})
	if appendErr != nil && !errors.Is(appendErr, aggregatestore.ErrEventsAppended) {
		r.mu.Unlock()
		return appendErr
	}

	r.stopped = true
	ownsProcessor := r.stopProcessor != nil
	r.mu.Unlock()

	if appendErr != nil {
		appendErr = staleHandleError("abandonment", appendErr)
	}

	// Clean up only when this handle owned the processor: a missing local
	// processor is no proof that none is running elsewhere — another process
	// may still be building this version, and tearing down beneath it races
	// its writes. Residue from a processor-less abandonment is cleaned up by
	// Begin when the version number is reused; a remote builder stops when
	// its next transition append conflicts with the Abandoned event.
	if !ownsProcessor {
		return appendErr
	}

	// Cleanup must not race the exiting processor: a final checkpoint save
	// landing after the delete would resurrect the checkpoint.
	if err := r.awaitProcessorStop(ctx); err != nil {
		return errors.Join(appendErr, err)
	}

	return errors.Join(appendErr, r.orchestrator.cleanup(ctx, state.Next))
}

// Retire tears down the previous version and records PreviousRetired,
// completing a successful rebuild. Act-then-append: teardown happens first,
// and retirement is recorded only after it succeeds. When the handler does
// not implement projection.Teardowner, removing the storage is the caller's
// responsibility and Retire records retirement after deleting the previous
// version's checkpoint.
func (r *Rebuild) Retire(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Refresh before acting: retirement destroys storage, so it must not
	// proceed from an arbitrarily stale view of the rebuild. A transition
	// landing after this refresh is still arbitrated at the final append,
	// but the destructive act deserves the narrowest possible window.
	if err := r.orchestrator.config.Rebuilds.Hydrate(ctx, r.aggregate, nil); err != nil {
		return fmt.Errorf("refreshing rebuild state: %w", err)
	}

	state := r.aggregate.State()

	switch {
	case state.Phase != PhasePromoted:
		return fmt.Errorf("cannot retire the previous version of a rebuild that is %s", state.Phase)
	case state.Previous.Version == 0:
		return errors.New("rebuild has no previous version to retire")
	}

	handler, err := r.orchestrator.config.Handler(state.Previous)
	if err != nil {
		return fmt.Errorf("creating handler for %s: %w", state.Previous, err)
	}

	if teardowner, ok := handler.(projection.Teardowner); ok {
		if err := teardowner.Teardown(ctx, state.Previous); err != nil {
			return fmt.Errorf("tearing down %s: %w", state.Previous, err)
		}
	}

	err = r.orchestrator.config.Checkpoints.Delete(ctx, state.Previous)
	if err != nil && !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		return fmt.Errorf("deleting checkpoint for %s: %w", state.Previous, err)
	}

	return r.appendLocked(ctx, PreviousRetired{Retired: state.Previous})
}

// appendLocked appends the transition to the aggregate and saves it. The save
// carries the aggregate's expected version, so competing orchestrators are
// arbitrated here: the loser's save reports a version mismatch, and the
// handle should be discarded and the rebuild reloaded via Resume. The caller
// must hold r.mu.
func (r *Rebuild) appendLocked(ctx context.Context, event estoria.DomainEvent[State]) error {
	r.aggregate.Append(event)

	if err := r.orchestrator.config.Rebuilds.Save(ctx, r.aggregate, nil); err != nil {
		return fmt.Errorf("recording %s: %w", event.EventType(), err)
	}

	return nil
}

// staleHandleError describes a transition that is durably recorded even
// though the aggregate could not observe it (aggregatestore.ErrEventsAppended):
// the action's effects have been applied, but the handle must be discarded
// and the rebuild resumed before further commands.
func staleHandleError(action string, err error) error {
	return fmt.Errorf("%s recorded, but the rebuild handle is stale; resume the rebuild before issuing further commands: %w", action, err)
}

// processorExit maps the processor's exit to Run's result: a processor
// stopped deliberately by a command (Abandon, Rollback) is not an error.
func (r *Rebuild) processorExit(err error) error {
	if r.isStopped() {
		return nil
	}

	return err
}

func (r *Rebuild) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.stopped
}
