package typeid

import (
	"errors"
	"strings"

	"github.com/gofrs/uuid/v5"
)

const defaultSep = "_"

type TypeID struct {
	prefix string
	suffix string
}

func From(prefix, suffix string) (TypeID, error) {
	switch {
	case prefix == "":
		return TypeID{}, errors.New("prefix is required")
	case suffix == "":
		return TypeID{}, errors.New("suffix is required")
	}

	return TypeID{prefix: prefix, suffix: suffix}, nil
}

func Must(id TypeID, err error) TypeID {
	if err != nil {
		panic(err)
	}

	return id
}

func (t TypeID) String() string {
	return t.prefix + defaultSep + t.suffix
}

func (t TypeID) Prefix() string {
	return t.prefix
}

func (t TypeID) Suffix() string {
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
		return TypeID{}, err
	}

	return From(prefix, id.String())
}

func (p DefaultParser) FromString(s string) (TypeID, error) {
	return p.parseWithSep(s, defaultSep)
}

func (p DefaultParser) parseWithSep(s, sep string) (TypeID, error) {
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return TypeID{}, errors.New("invalid type ID")
	}

	return From(parts[0], parts[1])
}
