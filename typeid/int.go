package typeid

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

type ValidInteger interface {
	~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64
}

// A Integer is a TypeID with an Integer value.
type Integer[T ValidInteger] interface {
	TypeID
	Integer() T
}

type integerID[T ValidInteger] struct {
	typ string
	val T
}

var (
	_ Integer[int]    = (*integerID[int])(nil)
	_ Integer[int32]  = (*integerID[int32])(nil)
	_ Integer[int64]  = (*integerID[int64])(nil)
	_ Integer[uint]   = (*integerID[uint])(nil)
	_ Integer[uint32] = (*integerID[uint32])(nil)
	_ Integer[uint64] = (*integerID[uint64])(nil)
)

func (id integerID[T]) String() string {
	return id.typ + defaultSep + id.Value()
}

func (id integerID[T]) TypeName() string {
	return id.typ
}

func (id integerID[T]) Value() string {
	return fmt.Sprint(id.val)
}

func (id integerID[T]) Integer() T {
	return id.val
}

// IntegerParser is a TypeID parser that generates and parses TypeIDs with Integers as values.
type IntegerParser[T ValidInteger] struct {
	newInteger func() (T, error)
	separator  string
}

func NewIntegerParser[T ValidInteger](opts ...IntegerParserOption[T]) IntegerParser[T] {
	p := IntegerParser[T]{
		newInteger: func() (T, error) {
			return T(rand.Int()), nil
		},
		separator: defaultSep,
	}

	for _, opt := range opts {
		opt(&p)
	}

	return p
}

func (p IntegerParser[T]) New(typ string) (TypeID, error) {
	id, err := p.newInteger()
	if err != nil {
		return nil, err
	}

	return integerID[T]{typ: typ, val: id}, nil
}

func (p IntegerParser[T]) ParseString(s string) (TypeID, error) {
	return p.parseWithSep(s, defaultSep)
}

func (p IntegerParser[T]) parseWithSep(s, sep string) (TypeID, error) {
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return nil, errors.New("invalid type ID")
	}

	val, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parsing integer: %w", err)
	}

	return integerID[T]{typ: parts[0], val: T(val)}, nil
}

type IntegerParserOption[T ValidInteger] func(*IntegerParser[T])

func WithIntegerCtor[T ValidInteger](fn func() (T, error)) IntegerParserOption[T] {
	return func(p *IntegerParser[T]) {
		p.newInteger = fn
	}
}

func WithSeparatorInteger[T ValidInteger](chars string) IntegerParserOption[T] {
	return func(p *IntegerParser[T]) {
		p.separator = chars
	}
}
