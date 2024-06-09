package typeid

import (
	"errors"
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
}

var _ UUID = (*uuidID)(nil)

func (id uuidID) String() string {
	return id.typ + defaultSep + id.val.String()
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

func NewUUIDParser(opts ...UUIDParserOption) UUIDParser {
	p := UUIDParser{
		newUUID:   defaultUUIDCtor,
		separator: defaultSep,
	}

	for _, opt := range opts {
		opt(&p)
	}

	return p
}

func (p UUIDParser) New(typ string) (TypeID, error) {
	if p.newUUID == nil {
		p.newUUID = uuid.NewV4
	}

	id, err := p.newUUID()
	if err != nil {
		return uuidID{}, err
	}

	return From(typ, id.String())
}

func (p UUIDParser) ParseString(s string) (TypeID, error) {
	return p.parseWithSep(s, defaultSep)
}

func (p UUIDParser) parseWithSep(s, sep string) (TypeID, error) {
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return uuidID{}, errors.New("invalid type ID")
	}

	return From(parts[0], parts[1])
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
