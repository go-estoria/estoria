package typeid

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

var defaultUUIDCtor = uuid.NewV4

// A UUID is a TypeID with a UUID value.
type UUID interface {
	TypeID
	UUID() uuid.UUID
}

type uuidID struct {
	typ string
	val uuid.UUID
	sep string
}

var _ UUID = (*uuidID)(nil)

func (id uuidID) String() string {
	return id.typ + id.sep + id.val.String()
}

func (id uuidID) TypeName() string {
	return id.typ
}

func (id uuidID) Value() string {
	return id.val.String()
}

func (id uuidID) UUID() uuid.UUID {
	return id.val
}

// UUIDParser is a TypeID parser that generates and parses TypeIDs with UUIDs as values.
type UUIDParser struct {
	newUUID   func() (uuid.UUID, error)
	separator string
}

func NewUUIDParser(opts ...UUIDParserOption) *UUIDParser {
	p := &UUIDParser{
		newUUID:   defaultUUIDCtor,
		separator: defaultSep,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

func (p *UUIDParser) New(typ string) (TypeID, error) {
	id, err := p.newUUID()
	if err != nil {
		return nil, err
	}

	return uuidID{typ: typ, val: id, sep: p.separator}, nil
}

func (p *UUIDParser) ParseString(s string) (TypeID, error) {
	return p.parseWithSep(s, p.separator)
}

func (p *UUIDParser) parseWithSep(s, sep string) (TypeID, error) {
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return uuidID{}, errors.New("invalid type ID")
	}

	id, err := uuid.FromString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parsing UUID: %w", err)
	}

	return uuidID{typ: parts[0], val: id, sep: p.separator}, nil
}

type UUIDParserOption func(*UUIDParser)

func WithUUIDCtor(fn func() (uuid.UUID, error)) UUIDParserOption {
	return func(p *UUIDParser) {
		p.newUUID = fn
	}
}

func WithSeparatorUUID(chars string) UUIDParserOption {
	return func(p *UUIDParser) {
		p.separator = chars
	}
}
