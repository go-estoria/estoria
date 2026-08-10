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

// TestParse_RoundTrip pins Parse as the inverse of String: any valid ID the
// library mints must survive the round trip through its string form, including
// one carrying the nil UUID, which String serializes like any other, and types
// with interior underscores, which the tail-anchored split keeps unambiguous.
func TestParse_RoundTrip(t *testing.T) {
	t.Parallel()

	for _, id := range []typeid.ID{
		typeid.NewV4("user"),
		typeid.NewV7("user"),
		typeid.New("usersnapshot", uuid.Must(uuid.NewV4())),
		typeid.New("user", uuid.Nil),
		typeid.New("funds_deposited", uuid.Must(uuid.NewV4())),
		typeid.New("user_account_v2", uuid.Must(uuid.NewV4())),
	} {
		parsed, err := typeid.Parse(id.String())
		if err != nil {
			t.Fatalf("parsing %q: %v", id.String(), err)
		}

		if parsed != id {
			t.Errorf("want %v, got %v", id, parsed)
		}
	}
}

// TestParse_RejectsNonCanonicalIDs pins that Parse accepts exactly what String
// produces and nothing else. The lenient UUID forms matter most: accepting a
// string String never produces means a parsed ID no longer serializes back to
// the input it came from, and two spellings address the same aggregate.
func TestParse_RejectsNonCanonicalIDs(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		id   string
	}{
		{"empty string", ""},
		{"no separator", "user"},
		{"empty type", "_9791012c-cd5b-4795-9c54-6085975d599b"},
		{"empty UUID", "user_"},
		{"malformed UUID", "user_not-a-uuid"},
		{"malformed 36-character UUID", "user_zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"},
		{"bare UUID with no type", "9791012c-cd5b-4795-9c54-6085975d599b"},
		{"no separator before the UUID", "user9791012c-cd5b-4795-9c54-6085975d599b"},
		{"leading underscore in the type", "_user_9791012c-cd5b-4795-9c54-6085975d599b"},
		{"trailing underscore in the type", "user__9791012c-cd5b-4795-9c54-6085975d599b"},
		{"braced UUID", "user_{9791012c-cd5b-4795-9c54-6085975d599b}"},
		{"URN UUID", "user_urn:uuid:9791012c-cd5b-4795-9c54-6085975d599b"},
		{"hashless UUID", "user_9791012ccd5b47959c546085975d599b"},
		{"uppercase UUID", "user_9791012C-CD5B-4795-9C54-6085975D599B"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if parsed, err := typeid.Parse(tt.id); err == nil {
				t.Errorf("want an error parsing %q, got %v", tt.id, parsed)
			}
		})
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
