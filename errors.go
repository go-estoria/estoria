package continuum

import "fmt"

type AggregateNotFoundError[E Entity] struct {
	Entity E
	ID     Identifier
}

func (e AggregateNotFoundError[E]) Error() string {
	return fmt.Sprintf("%s aggregate %s not found", e.Entity.AggregateType(), e.ID)
}
