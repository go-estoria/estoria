package continuum

import "fmt"

// A VersionSpec specifies a range of versions.
type VersionSpec struct {
	FromVersion int64
	ToVersion   int64
}

// Validate validates the version spec.
func (v VersionSpec) Validate() error {
	if v.FromVersion < 0 {
		return fmt.Errorf("from version must be >= 0")
	}

	if v.ToVersion < 0 {
		return fmt.Errorf("to version must be >= 0")
	}

	if v.FromVersion > v.ToVersion {
		return fmt.Errorf("from version must be <= to version")
	}

	return nil
}
