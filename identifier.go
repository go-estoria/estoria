package continuum

import "github.com/google/uuid"

type Identifier interface {
	Equals(other Identifier) bool
	String() string
}

type StringID string

func (id StringID) Equals(other Identifier) bool {
	otherStr, ok := other.(StringID)
	return ok && id == otherStr
}

func (id StringID) String() string {
	return string(id)
}

type IntID int

func (id IntID) Equals(other Identifier) bool {
	otherInt, ok := other.(IntID)
	return ok && id == otherInt
}

func (id IntID) String() string {
	return string(id)
}

type Int64ID int64

func (id Int64ID) Equals(other Identifier) bool {
	otherInt, ok := other.(Int64ID)
	return ok && id == otherInt
}

func (id Int64ID) String() string {
	return string(id)
}

type UUID uuid.UUID

func (id UUID) Equals(other Identifier) bool {
	otherUUID, ok := other.(UUID)
	return ok && id == otherUUID
}

func (id UUID) String() string {
	return id.String()
}
