package typeid

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

const defaultSep = "_"

var (
	intFactory    = NewIntegerFactory[uint64]()
	stringFactory = NewStringFactory()
	uuidFactory   = NewUUIDFactory()
)

type TypeID interface {
	TypeName() string
	Value() string
	String() string
}

func Must(id TypeID, err error) TypeID {
	if err != nil {
		panic(err)
	}

	return id
}

// For now, we only support UUID, string, and integer type IDs,
// and factories are set globally.
//
// This will eventually be replaced with per-component factory injection
// with sensible and/or coordinated defaults.

func SetIntFactory(f IntegerFactory[uint64]) {
	intFactory = f
}

func SetStringFactory(f StringFactory) {
	stringFactory = f
}

func SetUUIDFactory(f *UUIDFactory) {
	uuidFactory = f
}

func NewUUID(typ string) (UUID, error) {
	return uuidFactory.New(typ)
}

func NewString(typ string) (String, error) {
	return stringFactory.New(typ)
}

func NewInt[T ValidInteger](typ string) (Integer[uint64], error) {
	return intFactory.New(typ)
}

func ParseUUID(tid string) (UUID, error) {
	return uuidFactory.Parse(tid)
}

func ParseString(tid string) (String, error) {
	return stringFactory.Parse(tid)
}

func ParseInt(tid string) (Integer[uint64], error) {
	return intFactory.Parse(tid)
}

func FromUUID(typ string, id uuid.UUID) UUID {
	return uuidFactory.From(typ, id)
}

func FromString(typ, val string) TypeID {
	return stringFactory.From(typ, val)
}

func FromInt(typ string, val uint64) TypeID {
	return intFactory.From(typ, val)
}

func AsUUID(id TypeID) (UUID, error) {
	if id, ok := id.(UUID); ok {
		return id, nil
	}

	return nil, fmt.Errorf("TID type mismatch: expected UUID, got %T", id)
}

func AsString(id TypeID) (String, error) {
	if id, ok := id.(String); ok {
		return id, nil
	}

	return nil, fmt.Errorf("TID type mismatch: expected String, got %T", id)
}

func AsInt[T ValidInteger](id TypeID) (Integer[T], error) {
	if id, ok := id.(Integer[T]); ok {
		return id, nil
	}

	return nil, fmt.Errorf("TID type mismatch: expected Integer, got %T", id)
}
