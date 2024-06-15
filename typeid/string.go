package typeid

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
)

// A String is a TypeID with a string value.
type String interface {
	TypeID
}

type stringID struct {
	typ string
	val string
	sep string
}

func (id stringID) String() string {
	return id.typ + id.sep + id.Value()
}

func (id stringID) TypeName() string {
	return id.typ
}

func (id stringID) Value() string {
	return fmt.Sprint(id.val)
}

// StringFactory is a TypeID parser that generates and parses TypeIDs with strings as values.
type StringFactory struct {
	newString func() (string, error)
	separator string
}

func NewStringFactory(opts ...StringFactoryOption) StringFactory {
	p := StringFactory{
		newString: newRandomString,
		separator: defaultSep,
	}

	for _, opt := range opts {
		opt(&p)
	}

	return p
}

func (p StringFactory) New(typ string) (String, error) {
	id, err := p.newString()
	if err != nil {
		return nil, err
	}

	return p.From(typ, id), nil
}

func (p StringFactory) From(typ, val string) String {
	return stringID{typ: typ, sep: p.separator, val: val}
}

func (p StringFactory) Parse(tid string) (String, error) {
	parts := strings.Split(tid, p.separator)
	if len(parts) != 2 {
		return nil, errors.New("invalid type ID")
	}

	return p.From(parts[0], parts[1]), nil
}

type StringFactoryOption func(*StringFactory)

func WithStringGenerator(fn func() (string, error)) StringFactoryOption {
	return func(p *StringFactory) {
		p.newString = fn
	}
}

func WithStringSeparator(chars string) StringFactoryOption {
	return func(p *StringFactory) {
		p.separator = chars
	}
}

func newRandomString() (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 32)
	for i := range b {
		b[i] = charset[rand.IntN(len(charset))]
	}

	return string(b), nil
}
