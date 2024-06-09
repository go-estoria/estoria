package typeid

import (
	"errors"
)

const defaultSep = "_"

type TypeID interface {
	TypeName() string
	Value() string
	String() string
}

func From(typ, val string) (TypeID, error) {
	switch {
	case typ == "":
		return nil, errors.New("type is required")
	case val == "":
		return nil, errors.New("value is required")
	}

	return defaultParser.ParseString(typ + defaultSep + val)
}

func Must(id TypeID, err error) TypeID {
	if err != nil {
		panic(err)
	}

	return id
}

// A Parser knows how to create and parse TypeIDs.
type Parser interface {
	// New generates a new TypeID with the given type.
	New(typ string) (TypeID, error)

	// ParseString parses a string representation of a TypeID into a TypeID.
	ParseString(s string) (TypeID, error)
}

var defaultParser Parser = NewUUIDParser()

func SetDefaultParser(p Parser) {
	defaultParser = p
}

func New(typ string) (TypeID, error) {
	return defaultParser.New(typ)
}

func ParseString(s string) (TypeID, error) {
	return defaultParser.ParseString(s)
}
