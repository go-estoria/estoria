package aggregatestore_test

import (
	"testing"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/gofrs/uuid/v5"
)

// TestNew_ReservedStreamTypePrefix pins enforcement of the reserved "estoria."
// stream type namespace: user aggregate types must not carry it, while the
// library's own infrastructure types pass.
func TestNew_ReservedStreamTypePrefix(t *testing.T) {
	t.Parallel()

	events, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	newState := func(uuid.UUID) struct{} { return struct{}{} }

	t.Run("rejects a user aggregate type with the reserved prefix", func(t *testing.T) {
		t.Parallel()

		if _, err := aggregatestore.New(events, "estoria.custom", newState); err == nil {
			t.Error("want an error for a reserved-prefix aggregate type, got nil")
		}
	})

	t.Run("allows the library's own rebuild stream type", func(t *testing.T) {
		t.Parallel()

		if _, err := aggregatestore.New(events, "estoria.rebuild", newState); err != nil {
			t.Errorf("want the rebuild stream type allowed, got %v", err)
		}
	})

	t.Run("allows ordinary user aggregate types", func(t *testing.T) {
		t.Parallel()

		if _, err := aggregatestore.New(events, "account", newState); err != nil {
			t.Errorf("want an ordinary aggregate type allowed, got %v", err)
		}
	})
}
