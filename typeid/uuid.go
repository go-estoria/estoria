package typeid

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

var (
	defaultUUIDCtor   = uuid.NewV4
	defaultUUIDParser = uuid.FromString
)

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

func (id uuidID) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"%s"`, id.String())), nil
}

func (id *uuidID) UnmarshalJSON(data []byte) error {
	tid, err := uuidFactory.Parse(string(data[1 : len(data)-1]))
	if err != nil {
		return fmt.Errorf("parsing UUID TypeID: %w", err)
	}

	*id = tid.(uuidID)

	return nil
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

// UUIDFactory is a TypeID parser that generates and parses TypeIDs with UUIDs as values.
type UUIDFactory struct {
	newUUID   func() (uuid.UUID, error)
	parseUUID func(string) (uuid.UUID, error)
	separator string
}

func NewUUIDFactory(opts ...UUIDParserOption) *UUIDFactory {
	p := &UUIDFactory{
		newUUID:   defaultUUIDCtor,
		parseUUID: defaultUUIDParser,
		separator: defaultSep,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

func (p *UUIDFactory) New(typ string) (UUID, error) {
	id, err := p.newUUID()
	if err != nil {
		return nil, err
	}

	return FromUUID(typ, id), nil
}

func (p *UUIDFactory) From(typ string, id uuid.UUID) UUID {
	return uuidID{typ: typ, val: id, sep: p.separator}
}

func (p *UUIDFactory) Parse(tid string) (UUID, error) {
	parts := strings.Split(tid, p.separator)
	if len(parts) != 2 {
		return uuidID{}, errors.New("invalid type ID")
	}

	id, err := uuid.FromString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parsing UUID: %w", err)
	}

	return FromUUID(parts[0], id), nil
}

type UUIDParserOption func(*UUIDFactory)

func WithUUIDGenerator(fn func() (uuid.UUID, error)) UUIDParserOption {
	return func(p *UUIDFactory) {
		p.newUUID = fn
	}
}

func WithUUIDParser(fn func(string) (uuid.UUID, error)) UUIDParserOption {
	return func(p *UUIDFactory) {
		p.parseUUID = fn
	}
}

func WithUUIDSeparator(chars string) UUIDParserOption {
	return func(p *UUIDFactory) {
		p.separator = chars
	}
}
