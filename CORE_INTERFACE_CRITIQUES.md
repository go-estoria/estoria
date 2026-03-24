# Core Estoria Interface Critiques

These findings were surfaced during a production-readiness review of the `estoria-contrib/postgres` event store and outbox implementation (March 2026). They reflect design concerns in the core `estoria` module that affect correctness, portability, or production readiness across implementations.

## Resolution Status (March 2026)

All six critiques (C1-C6) have been addressed in the core interface:

**C1 (Offset Semantics)** — Resolved via rename to `AfterVersion int64` with explicit forward/reverse semantics documented.
- Forward reads return events with `StreamVersion > AfterVersion` (exclusive lower bound)
- Reverse reads return events with `StreamVersion <= AfterVersion` (inclusive upper bound)
- Default 0 = beginning (forward) or end (reverse)

**C2 (ExpectVersion Ambiguity)** — Resolved via nullable pointer type and dedicated field.
- Changed `ExpectVersion int64` to `ExpectVersion *int64` (nil = no check)
- Added `StreamMustNotExist bool` field (mutually exclusive with `ExpectVersion`)
- Added `VersionPtr(v int64) *int64` convenience function

**C3 (No Global Position)** — Resolved via optional field.
- Added `GlobalPosition *int64` to `Event` struct
- Nil for backends without global ordering (S3); populated for those with it (Postgres, MongoDB)

**C4 (No Event Metadata)** — Resolved via map field on both types.
- Added `Metadata map[string]string` to `Event` struct
- Added `Metadata map[string]string` to `WritableEvent` struct
- Stored alongside event data in all backends

**C5 (ErrEventExists Portability)** — Resolved via move to core.
- `EventExistsError` now defined in core `eventstore` package (renamed from `ErrEventExists`)
- Allows portable error handling across all implementations

**C6 (Error Type Checking)** — Resolved via `Is()` methods.
- All error types (`StreamVersionMismatchError`, `EventMarshalingError`, `EventUnmarshalingError`, `InitializationError`, `EventExistsError`) now have `Is()` methods
- Enables type-safe checking with `errors.Is(err, ErrorType{})`

---

## C1: `ReadStreamOptions.Offset` Semantics Are Ambiguous

**File:** `estoria/eventstore/event_store.go:37`

**Current documentation:**
```
Offset is the starting position in the stream (exclusive).
Default: 0 (beginning of stream)
```

**Issue:** "Starting position (exclusive)" and SQL `OFFSET` (number of rows to skip) happen to produce the same result for contiguous forward reads, but they are fundamentally different concepts. The Postgres implementation uses SQL `OFFSET`, which skips N rows from the ordered result set. An implementation that interprets Offset as a stream version/position would behave differently, especially for reverse reads or streams with non-contiguous versions.

**Risk:** Multiple implementations of `StreamReader` may interpret Offset differently, producing divergent behavior for the same interface. Code portable across backends would silently return wrong results.

**Recommendation:** Either:
- Rename `Offset` to `Skip` to match SQL semantics and update documentation, or
- Define `Offset` explicitly as a stream version (position-based) and require implementations to translate accordingly, or
- Introduce separate `Skip int64` and `AfterVersion int64` fields with clear semantics

## C2: `ExpectVersion=0` Cannot Express "Stream Must Not Exist"

**File:** `estoria/eventstore/event_store.go:73-77`

**Current documentation:**
```
ExpectVersion specifies the expected latest version of the stream when appending events.
Default: 0 (no expectation)
```

**Issue:** `0` is used as both "I don't care about the version" and "the stream should be at version 0 (empty)." There is no way for a caller to express "this stream must not exist yet" — the first-write-wins guarantee for new streams is absent. Two concurrent "create stream" appends both with `ExpectVersion=0` will both succeed.

**Risk:** Race condition on stream creation. Event-sourced aggregates that rely on optimistic concurrency for initial creation have no protection against duplicate creates.

**Recommendation:** Either:
- Use a sentinel value (e.g., `-1`) for "no expectation" and let `0` mean "stream must be empty," or
- Add a separate `MustNotExist bool` field to `AppendStreamOptions`, or
- Document that `ExpectVersion=0` means "no expectation" and provide guidance for idempotent stream creation at the application level

## C3: No Global Position in `Event` Struct

**File:** `estoria/eventstore/event_store.go:81-87`

**Current `Event` struct:**
```go
type Event struct {
    ID            typeid.ID
    StreamID      typeid.ID
    StreamVersion int64
    Timestamp     time.Time
    Data          []byte
}
```

**Issue:** The struct has `StreamVersion` (position within a single stream) but no global position/sequence number. The Postgres schema has a `bigserial id` column that provides global ordering, and `ReadAll` orders by this column internally, but consumers cannot access the global position. This prevents:
- Catch-up subscriptions ("read all events after global position X")
- Cross-stream event correlation by insertion order
- Distributed projection checkpointing

**Recommendation:** Add an optional `GlobalPosition int64` field (or `*int64` to indicate "not available"). Implementations that support global ordering populate it; others leave it zero/nil.

## C4: No Event Metadata or Headers

**Files:** `estoria/eventstore/event_store.go:81-95`

**Issue:** Neither `Event` nor `WritableEvent` has a field for metadata, headers, or attributes beyond the core fields. Production event-sourced systems typically need:
- **Correlation ID** — links events across aggregates/services in a workflow
- **Causation ID** — identifies the event or command that caused this event
- **User/actor ID** — who performed the action
- **Trace context** — distributed tracing (OpenTelemetry) propagation

Currently, applications must encode metadata inside the event `Data` payload, mixing infrastructure concerns with domain data and making cross-cutting metadata extraction impossible without deserializing every event.

**Recommendation:** Add a `Metadata map[string]string` field (or `[]byte` for arbitrary structured metadata) to both `Event` and `WritableEvent`. Implementations store it alongside event data (e.g., a separate `metadata jsonb` column in Postgres).

## C5: `ErrEventExists` Should Be a Core Error Type

**File:** `estoria-contrib/postgres/eventstore/errors.go` (currently Postgres-specific)

**Issue:** Duplicate event detection (idempotent writes) is a cross-cutting concern relevant to any event store implementation. Currently, `ErrEventExists` is defined only in the Postgres contrib package. Code written against the core `eventstore.Store` interface cannot portably detect or handle duplicate event errors without importing an implementation-specific package.

Compare with `StreamVersionMismatchError`, which IS defined in the core `eventstore` package and can be used portably.

**Recommendation:** Define a core `ErrEventExists` error type in `estoria/eventstore/event_store.go`, parallel to `StreamVersionMismatchError`. Implementations return this type when a duplicate event ID is detected. This also requires adding a unique constraint on `event_id` to the Postgres schema (currently absent).

## C6: `StreamVersionMismatchError` Lacks `Is()` Method

**File:** `estoria/eventstore/event_store.go:126-137`

**Current type:**
```go
type StreamVersionMismatchError struct {
    StreamID        typeid.ID
    ExpectedVersion int64
    ActualVersion   int64
}
```

**Issue:** This error type works with `errors.As()` (type assertion) but behaves unexpectedly with `errors.Is()`. Since it has no `Is()` method, `errors.Is(err, StreamVersionMismatchError{})` only matches if ALL fields are identical (struct value equality). A caller checking `errors.Is(err, StreamVersionMismatchError{})` with a zero-value sentinel will never match a real error (which has non-zero fields). This forces callers to use `errors.As()` exclusively, which is correct but non-obvious.

The same issue applies to `EventMarshalingError`, `EventUnmarshalingError`, and `InitializationError`.

**Recommendation:** Either:
- Add an `Is(target error) bool` method that matches on type only (ignoring field values), allowing `errors.Is(err, StreamVersionMismatchError{})` to work as a type check, or
- Document clearly that these errors must be checked with `errors.As()`, not `errors.Is()`, or
- Provide sentinel values like `var ErrStreamVersionMismatch = errors.New("stream version mismatch")` and have the struct type wrap it via `Unwrap()`

---

## Priority

| # | Impact | Effort | Suggested Priority |
|---|--------|--------|--------------------|
| C1 | High — portability risk across backends | Medium — API change | P1 |
| C2 | High — correctness gap for concurrent creates | Low — small API addition | P1 |
| C3 | Medium — blocks subscriptions and projections | Low — additive field | P2 |
| C4 | Medium — blocks production tracing/correlation | Medium — schema + API change | P2 |
| C5 | Low — workaround exists (import impl package) | Low — move error type | P3 |
| C6 | Low — workaround exists (use errors.As) | Low — add Is() method | P3 |
