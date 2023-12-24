package continuum

import "github.com/google/uuid"

type Identifier interface {
	Equals(other Identifier) bool
}

type StringID string

func (id StringID) Equals(other Identifier) bool {
	otherStr, ok := other.(StringID)
	return ok && id == otherStr
}

type IntID int

func (id IntID) Equals(other Identifier) bool {
	otherInt, ok := other.(IntID)
	return ok && id == otherInt
}

type Int64ID int64

func (id Int64ID) Equals(other Identifier) bool {
	otherInt, ok := other.(Int64ID)
	return ok && id == otherInt
}

type UUID uuid.UUID

func (id UUID) Equals(other Identifier) bool {
	otherUUID, ok := other.(UUID)
	return ok && id == otherUUID
}
