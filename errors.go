package continuum

import "fmt"

type AggregateNotFoundError[AT Entity] struct {
	AggregateType AT
	ID            string
}

func (e AggregateNotFoundError[AT]) Error() string {
	return fmt.Sprintf("%s aggregate %s not found", e.AggregateType.AggregateTypeName(), e.ID)
}
