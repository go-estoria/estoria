package typeid

import (
	"errors"
	"strings"

	"github.com/gofrs/uuid/v5"
)

const sep = "_"

type TypeID struct {
	prefix string
	suffix string
}

func From(prefix, suffix string) (TypeID, error) {
	return TypeID{prefix: prefix, suffix: suffix}, nil
}

func (t TypeID) String() string {
	return t.prefix + sep + t.suffix
}

func (t TypeID) Prefix() string {
	return t.prefix
}

func (t TypeID) Suffix() string {
	return t.suffix
}

func WithPrefix(prefix string) (TypeID, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return TypeID{}, err
	}

	return From(prefix, id.String())
}

func Must(id TypeID, err error) TypeID {
	if err != nil {
		panic(err)
	}

	return id
}

func FromString(s string) (TypeID, error) {
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return TypeID{}, errors.New("invalid type ID")
	}

	return From(parts[0], parts[1])
}
