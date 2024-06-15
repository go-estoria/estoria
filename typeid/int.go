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

// IntegerFactory is a TypeID parser that generates and parses TypeIDs with Integers as values.
type IntegerFactory[T ValidInteger] struct {
	newInteger func() (T, error)
	separator  string
}

func NewIntegerFactory[T ValidInteger](opts ...IntegerFactoryOption[T]) IntegerFactory[T] {
	p := IntegerFactory[T]{
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

func (p IntegerFactory[T]) New(typ string) (TypeID, error) {
	id, err := p.newInteger()
	if err != nil {
		return nil, err
	}

	return p.From(typ, id), nil
}

func (p IntegerFactory[T]) From(typ string, val T) Integer[T] {
	return integerID[T]{typ: typ, val: val}
}

func (p IntegerFactory[T]) Parse(tid string) (Integer[T], error) {
	parts := strings.Split(tid, p.separator)
	if len(parts) != 2 {
		return nil, errors.New("invalid type ID")
	}

	val, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parsing integer: %w", err)
	}

	return p.From(parts[0], T(val)), nil
}

type IntegerFactoryOption[T ValidInteger] func(*IntegerFactory[T])

func WithIntegerCtor[T ValidInteger](fn func() (T, error)) IntegerFactoryOption[T] {
	return func(p *IntegerFactory[T]) {
		p.newInteger = fn
	}
}

func WithIntegerSeparator[T ValidInteger](chars string) IntegerFactoryOption[T] {
	return func(p *IntegerFactory[T]) {
		p.separator = chars
	}
}
