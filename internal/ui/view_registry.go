package ui

import "github.com/ianbruene/ddgo/internal/app"

// eventView is a UI surface which presents controller state and events.
// Implementations may be backed by any UI toolkit; the registry deliberately is not.
type eventView interface {
	applyState(app.State)
	applyEvent(app.Event)
}

type viewID uint64

type viewRegistry struct {
	nextID viewID
	views  map[viewID]eventView
}

func (r *viewRegistry) add(view eventView) viewID {
	if r.views == nil {
		r.views = make(map[viewID]eventView)
	}
	r.nextID++
	r.views[r.nextID] = view
	return r.nextID
}

func (r *viewRegistry) remove(id viewID) {
	delete(r.views, id)
}

func (r *viewRegistry) applyState(state app.State) {
	for _, view := range r.views {
		view.applyState(state)
	}
}

func (r *viewRegistry) applyEvent(event app.Event) {
	for _, view := range r.views {
		view.applyEvent(event)
	}
}
