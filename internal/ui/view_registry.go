package ui

import "github.com/ianbruene/ddgo/internal/app"

// eventView is a UI surface that presents application state and reacts to
// application events. Implementations may be backed by any UI toolkit; the
// registry deliberately is not.
//
// applyState renders the newest controller state accepted by the UI
// dispatcher. Older event snapshots may be ignored.
//
// applyEvent handles event-specific behavior. Every event is delivered even
// when its attached State snapshot is older than the state already presented.
type eventView interface {
	applyState(app.State)
	applyEvent(app.Event)
}

// viewDispatcher owns the state that has entered the UI event stream and
// distributes state and event-specific behavior to every registered view.
type viewDispatcher struct {
	state    app.State
	revision app.StateRevision
	views    viewRegistry
}

func (d *viewDispatcher) register(view eventView) viewID {
	id := d.views.add(view)
	view.applyState(d.state)
	return id
}

func (d *viewDispatcher) unregister(id viewID) {
	d.views.remove(id)
}

func (d *viewDispatcher) dispatch(event app.Event) {
	if event.StateRevision > d.revision {
		d.state = event.State
		d.revision = event.StateRevision
		d.views.applyState(event.State)
	}
	d.views.applyEvent(event)
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
