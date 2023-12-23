package continuum

import "fmt"

type AggregateNotFoundError struct {
	Type string
	ID   string
}

func (e AggregateNotFoundError) Error() string {
	return fmt.Sprintf("%s aggregate %s not found", e.Type, e.ID)
}
