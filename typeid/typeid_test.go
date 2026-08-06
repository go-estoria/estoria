package typeid_test

import (
	"testing"
	"time"

	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

func TestID_String(t *testing.T) {
	t.Parallel()

	uid := uuid.Must(uuid.FromString("9791012c-cd5b-4795-9c54-6085975d599b"))

	// The format is load-bearing beyond display: the event-stream snapshot store derives a
	// stream name from it, and backends key storage on it.
	if want, got := "user_9791012c-cd5b-4795-9c54-6085975d599b", typeid.New("user", uid).String(); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	uid := uuid.Must(uuid.NewV4())

	id := typeid.New("user", uid)
	if id.Type != "user" {
		t.Errorf("want type %q, got %q", "user", id.Type)
	}

	if id.UUID != uid {
		t.Errorf("want UUID %s, got %s", uid, id.UUID)
	}
}

func TestNewV4(t *testing.T) {
	t.Parallel()

	id := typeid.NewV4("user")
	if id.Type != "user" {
		t.Errorf("want type %q, got %q", "user", id.Type)
	}

	if got := id.UUID.Version(); got != 4 {
		t.Errorf("want a v4 UUID, got version %d", got)
	}

	if id.UUID.IsNil() {
		t.Error("want a generated UUID, got the zero value")
	}

	if other := typeid.NewV4("user"); other.UUID == id.UUID {
		t.Error("want distinct UUIDs from successive calls, got the same one twice")
	}
}

func TestNewV7(t *testing.T) {
	t.Parallel()

	id := typeid.NewV7("user")
	if id.Type != "user" {
		t.Errorf("want type %q, got %q", "user", id.Type)
	}

	// v7 rather than v4 is the whole reason this constructor exists: the version nibble is
	// what makes the ID time-ordered and therefore k-sortable in an index.
	if got := id.UUID.Version(); got != 7 {
		t.Errorf("want a v7 UUID, got version %d", got)
	}
}

// TestNewV7_Sortable pins the property callers actually choose v7 for: IDs minted in
// different milliseconds sort in creation order. That is the granularity of v7's
// guarantee — within a single millisecond the generator's monotonic counter is
// best-effort only (its visible 12 bits can wrap), so same-millisecond order is
// deliberately not asserted here.
func TestNewV7_Sortable(t *testing.T) {
	t.Parallel()

	previous := typeid.NewV7("user")
	for range 20 {
		// Sleeping at least 1ms guarantees the next ID carries a later timestamp.
		time.Sleep(time.Millisecond)

		current := typeid.NewV7("user")
		if current.UUID.String() <= previous.UUID.String() {
			t.Fatalf("want v7 IDs to sort in creation order, got %s after %s", current.UUID, previous.UUID)
		}

		previous = current
	}
}
