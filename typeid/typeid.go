package typeid

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// An ID is a UUID with an associated type name.
type ID struct {
	Type string
	UUID uuid.UUID
}

// String returns the string representation of the ID in the format "type_uuid".
//
// Example: "user_9791012c-cd5b-4795-9c54-6085975d599b".
func (id ID) String() string {
	return id.Type + "_" + id.UUID.String()
}

// ValidateTypeName reports whether name is a valid type name for an ID: it
// must be non-empty and must neither start nor end with an underscore.
func ValidateTypeName(name string) error {
	switch {
	case name == "":
		return errors.New("type name is required")
	case strings.HasPrefix(name, "_") || strings.HasSuffix(name, "_"):
		return errors.New("type name must not start or end with '_'")
	}

	return nil
}

// The canonical UUID form is 36 characters, so the shortest possible ID is a
// one-character type, the separator, and the UUID.
const minIDLength = 1 + 1 + 36

// Parse parses the string representation of an ID in the format "type_uuid",
// so that IDs can round-trip through paths, query strings, and logs.
func Parse(s string) (ID, error) {
	if len(s) < minIDLength {
		return ID{}, fmt.Errorf("invalid ID %q: too short for the \"type_uuid\" form", s)
	}

	typeName, uuidStr := s[:len(s)-37], s[len(s)-36:]

	if s[len(s)-37] != '_' {
		return ID{}, fmt.Errorf("invalid ID %q: missing '_' separator before the UUID", s)
	}

	if err := ValidateTypeName(typeName); err != nil {
		return ID{}, fmt.Errorf("invalid ID %q: %w", s, err)
	}

	uid, err := uuid.FromString(uuidStr)
	if err != nil {
		return ID{}, fmt.Errorf("invalid ID %q: parsing UUID: %w", s, err)
	}

	// FromString is lenient — uppercase and other 36-character near-misses can
	// parse — but those are strings String never produces. Requiring the
	// canonical form keeps Parse an exact inverse of String, so a parsed ID
	// always serializes back to the input it came from.
	if uid.String() != uuidStr {
		return ID{}, fmt.Errorf("invalid ID %q: UUID is not in the canonical form", s)
	}

	return ID{Type: typeName, UUID: uid}, nil
}

// NewV4 creates a new ID with the given type name and a new v4 UUID.
//
// UUID v4 is a randomly generated UUID.
func NewV4(typeName string) ID {
	return ID{Type: typeName, UUID: uuid.Must(uuid.NewV4())}
}

// NewV7 creates a new ID with the given type name and a new v7 UUID.
//
// UUID v7 is a time-ordered, k-sortable UUID.
func NewV7(typeName string) ID {
	return ID{Type: typeName, UUID: uuid.Must(uuid.NewV7())}
}

// New creates a new ID with the given type name and UUID.
func New(typeName string, uid uuid.UUID) ID {
	return ID{Type: typeName, UUID: uid}
}
