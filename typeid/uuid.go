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
	sep               = "_"
)

// A UUID is a TypeID with a UUID value.
type UUID struct {
	typ string
	val uuid.UUID
}

func (id UUID) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, `"%s"`, id.String()), nil
}

func (id *UUID) UnmarshalJSON(data []byte) error {
	tid, err := uuidFactory.Parse(string(data[1 : len(data)-1]))
	if err != nil {
		return fmt.Errorf("parsing UUID TypeID: %w", err)
	}

	*id = tid

	return nil
}

func (id UUID) MarshalText() ([]byte, error) {
	return []byte(id.String()), nil
}

func (id *UUID) UnmarshalText(data []byte) error {
	tid, err := ParseUUID(string(data))
	if err != nil {
		return fmt.Errorf("parsing UUID TypeID: %w", err)
	}

	*id = tid

	return nil
}

func (id UUID) MarshalBinary() ([]byte, error) {
	return []byte(id.String()), nil
}

func (id *UUID) UnmarshalBinary(data []byte) error {
	tid, err := ParseUUID(string(data))
	if err != nil {
		return fmt.Errorf("parsing UUID TypeID: %w", err)
	}

	*id = tid

	return nil
}

func (id UUID) IsEmpty() bool {
	return id.typ == "" || id.val.IsNil()
}

func (id UUID) String() string {
	return id.typ + sep + id.val.String()
}

func (id UUID) TypeName() string {
	return id.typ
}

func (id UUID) Value() string {
	return id.val.String()
}

func (id UUID) UUID() uuid.UUID {
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
		return UUID{}, err
	}

	return FromUUID(typ, id), nil
}

func (p *UUIDFactory) From(typ string, id uuid.UUID) UUID {
	return UUID{typ: typ, val: id}
}

func (p *UUIDFactory) Parse(tid string) (UUID, error) {
	parts := strings.Split(tid, sep)
	if len(parts) != 2 {
		return UUID{}, errors.New("invalid type ID")
	}

	id, err := uuid.FromString(parts[1])
	if err != nil {
		return UUID{}, fmt.Errorf("parsing UUID: %w", err)
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
