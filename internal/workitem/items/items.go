// Package items registers every kind of work item, once, in the order they
// prepare a run: a ticket names the run's branch first, a merge request
// then puts its checkout on its own. A new kind is a line here.
package items

import (
	"apothecary/internal/workitem"
	"apothecary/internal/workitem/mergerequest"
	"apothecary/internal/workitem/ticket"
)

// All is the registry of every kind.
func All() *workitem.Registry {
	r, err := workitem.NewRegistry(ticket.Kind{}, mergerequest.Kind{})
	if err != nil {
		panic(err) // the list above is fixed: a duplicate is a bug here
	}
	return r
}
