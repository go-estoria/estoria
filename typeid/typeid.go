package typeid

import (
	"errors"
	"strings"

	"github.com/gofrs/uuid/v5"
)

const defaultSep = "_"

type TypeID interface {
	Prefix() string
	Suffix() string
	String() string
}

type typeID struct {
	prefix string
	suffix string
}

func From(prefix, suffix string) (TypeID, error) {
	switch {
	case prefix == "":
		return typeID{}, errors.New("prefix is required")
	case suffix == "":
		return typeID{}, errors.New("suffix is required")
	}

	return typeID{prefix: prefix, suffix: suffix}, nil
}

func Must(id TypeID, err error) TypeID {
	if err != nil {
		panic(err)
	}

	return id
}

func (t typeID) String() string {
	return t.prefix + defaultSep + t.suffix
}

func (t typeID) Prefix() string {
	return t.prefix
}

func (t typeID) Suffix() string {
	return t.suffix
}

func NewWithType(prefix string) (TypeID, error) {
	return defaultParser.NewWithType(prefix)
}

func FromString(s string) (TypeID, error) {
	return defaultParser.FromString(s)
}

type Parser interface {
	NewWithType(prefix string) (TypeID, error)
	FromString(s string) (TypeID, error)
}

type DefaultParser struct{}

var defaultParser DefaultParser

func (p DefaultParser) NewWithType(prefix string) (TypeID, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return typeID{}, err
	}

	return From(prefix, id.String())
}

func (p DefaultParser) FromString(s string) (TypeID, error) {
	return p.parseWithSep(s, defaultSep)
}

func (p DefaultParser) parseWithSep(s, sep string) (TypeID, error) {
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return typeID{}, errors.New("invalid type ID")
	}

	return From(parts[0], parts[1])
}
