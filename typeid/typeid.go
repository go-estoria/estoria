package typeid

import (
	"github.com/gofrs/uuid/v5"
)

const defaultSep = "_"

var (
	uuidFactory = NewUUIDFactory()
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

func SetUUIDFactory(f *UUIDFactory) {
	uuidFactory = f
}

func NewUUID(typ string) (UUID, error) {
	return uuidFactory.New(typ)
}

func ParseUUID(tid string) (UUID, error) {
	return uuidFactory.Parse(tid)
}

func FromUUID(typ string, id uuid.UUID) UUID {
	return uuidFactory.From(typ, id)
}
