package continuum

import "fmt"

// AggregateNotFoundError is returned when an aggregate is not found.
type AggregateNotFoundError[E Entity] struct {
	Entity E
	ID     Identifier
}

// Error returns the error message.
func (e AggregateNotFoundError[E]) Error() string {
	return fmt.Sprintf("%s aggregate %s not found", e.Entity.AggregateType(), e.ID)
}
