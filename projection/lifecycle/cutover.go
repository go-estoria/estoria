package lifecycle

import (
	"context"

	"github.com/go-estoria/estoria/projection"
)

// A Cutover is one recorded read-route flip: the version now serving reads
// and the projection's cutover revision that recorded it. The revision is
// what makes redelivery and competition decidable for setters — versions are
// not monotonic across rollbacks, revisions are.
type Cutover struct {
	// Live is the version the flip put in service. Its name identifies the
	// projection the cutover belongs to.
	Live projection.ID

	// Revision is the projection's monotonic cutover revision, 1-based from
	// the first promotion, as recorded by the Promoted or RolledBack event.
	Revision int64
}

// A CutoverSetter is the managed write side of the cutover — a routing
// pointer (a postgres row, a redis key) that directs reads to the live
// version. The recorded event is authoritative; setters are caches of it,
// converged by the cutover worker and consulted as retirement witnesses.
//
// ApplyCutover must be apply-if-newer, atomically per projection name: a
// delivery with a revision above the stored one updates the physical target
// and the stored (Live, Revision) together, as one durable operation; an
// equal revision with the same live version is an idempotent no-op; an
// equal revision with a different live version is a corruption error — two
// cutovers cannot share a revision; a lower revision is a stale no-op,
// never an error, so redelivery cannot wedge the worker. The atomicity is
// what the contract buys: a setter that stores the pair separately can
// serve a route it will not vouch for, or vouch for a route it does not
// serve.
//
// AppliedCutover reports the cutover the setter currently serves for the
// named projection — the route actually directing traffic, not an intent
// or a cached copy of the stream. It reports ErrNoLiveVersion (wrapped)
// when no cutover has ever been applied for the name.
type CutoverSetter interface {
	ApplyCutover(ctx context.Context, cutover Cutover) error
	AppliedCutover(ctx context.Context, name string) (Cutover, error)
}
