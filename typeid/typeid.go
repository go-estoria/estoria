package typeid

import (
	"errors"
	"strings"

	"github.com/gofrs/uuid/v5"
)

const defaultSep = "_"

type TypeID interface {
	TypeName() string
	Value() string
	String() string
}

type typeID struct {
	typ string
	val string
}

func From(typ, val string) (TypeID, error) {
	switch {
	case typ == "":
		return typeID{}, errors.New("typ is required")
	case val == "":
		return typeID{}, errors.New("val is required")
	}

	return typeID{typ: typ, val: val}, nil
}

func Must(id TypeID, err error) TypeID {
	if err != nil {
		panic(err)
	}

	return id
}

func (t typeID) String() string {
	return t.typ + defaultSep + t.val
}

func (t typeID) TypeName() string {
	return t.typ
}

func (t typeID) Value() string {
	return t.val
}

func New(typ string) (TypeID, error) {
	return defaultParser.New(typ)
}

func ParseString(s string) (TypeID, error) {
	return defaultParser.ParseString(s)
}

type Parser interface {
	New(typ string) (TypeID, error)
	ParseString(s string) (TypeID, error)
}

type DefaultParser struct{}

var defaultParser DefaultParser

func (p DefaultParser) New(typ string) (TypeID, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return typeID{}, err
	}

	return From(typ, id.String())
}

func (p DefaultParser) ParseString(s string) (TypeID, error) {
	return p.parseWithSep(s, defaultSep)
}

func (p DefaultParser) parseWithSep(s, sep string) (TypeID, error) {
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return typeID{}, errors.New("invalid type ID")
	}

	return From(parts[0], parts[1])
}
