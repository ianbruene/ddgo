package ui

import (
	"reflect"
	"testing"

	"github.com/ianbruene/ddgo/internal/app"
)

type recordingView struct {
	states []app.State
	events []app.Event
	calls  []string
}

func (v *recordingView) applyState(state app.State) {
	v.states = append(v.states, state)
	v.calls = append(v.calls, "state")
}
func (v *recordingView) applyEvent(event app.Event) {
	v.events = append(v.events, event)
	v.calls = append(v.calls, "event")
}

func TestViewRegistryBroadcastAndRemove(t *testing.T) {
	t.Parallel()

	var registry viewRegistry
	first, second := &recordingView{}, &recordingView{}
	firstID := registry.add(first)
	registry.add(second)

	event := app.Event{Kind: app.EventStateChanged, Text: "connected"}
	registry.applyEvent(event)
	if len(first.events) != 1 || !reflect.DeepEqual(first.events[0], event) {
		t.Fatalf("first view events = %#v, want %#v", first.events, []app.Event{event})
	}
	if len(second.events) != 1 || !reflect.DeepEqual(second.events[0], event) {
		t.Fatalf("second view events = %#v, want %#v", second.events, []app.Event{event})
	}

	registry.remove(firstID)
	registry.applyEvent(event)
	if len(first.events) != 1 {
		t.Fatalf("removed view received %d events, want 1", len(first.events))
	}
	if len(second.events) != 2 {
		t.Fatalf("remaining view received %d events, want 2", len(second.events))
	}
}

func TestViewDispatcherDispatchesStateBeforeEventToEveryView(t *testing.T) {
	t.Parallel()

	initial := app.State{PortName: "initial"}
	next := app.State{Connected: true, PortName: "next"}
	dispatcher := viewDispatcher{state: initial}
	first, second := &recordingView{}, &recordingView{}
	dispatcher.register(first)
	dispatcher.register(second)
	first.calls, second.calls = nil, nil

	event := app.Event{Kind: app.EventStateChanged, State: next, StateRevision: 1}
	dispatcher.dispatch(event)

	for name, view := range map[string]*recordingView{"first": first, "second": second} {
		if !reflect.DeepEqual(view.calls, []string{"state", "event"}) {
			t.Errorf("%s view calls = %v, want [state event]", name, view.calls)
		}
		if !reflect.DeepEqual(view.states[len(view.states)-1], next) {
			t.Errorf("%s view latest state = %#v, want %#v", name, view.states[len(view.states)-1], next)
		}
		if !reflect.DeepEqual(view.events, []app.Event{event}) {
			t.Errorf("%s view events = %#v, want %#v", name, view.events, []app.Event{event})
		}
	}
}

func TestViewDispatcherLateRegistrationUsesDispatchedState(t *testing.T) {
	t.Parallel()

	initial := app.State{PortName: "initial"}
	next := app.State{Connected: true, PortName: "next"}
	dispatcher := viewDispatcher{state: initial}
	first := &recordingView{}
	dispatcher.register(first)
	if !reflect.DeepEqual(first.states, []app.State{initial}) {
		t.Fatalf("initial view states = %#v, want %#v", first.states, []app.State{initial})
	}

	dispatcher.dispatch(app.Event{Kind: app.EventStateChanged, State: next, StateRevision: 1})
	if !reflect.DeepEqual(dispatcher.state, next) {
		t.Fatalf("dispatcher state = %#v, want %#v", dispatcher.state, next)
	}

	late := &recordingView{}
	dispatcher.register(late)
	if !reflect.DeepEqual(late.states, []app.State{next}) {
		t.Fatalf("late view states = %#v, want %#v", late.states, []app.State{next})
	}
	if len(late.events) != 0 {
		t.Fatalf("late view received %d past events, want 0", len(late.events))
	}
}

func TestViewDispatcherRejectsStaleStateButDeliversEveryEvent(t *testing.T) {
	t.Parallel()

	dispatcher := viewDispatcher{state: app.State{ProgramComplete: 10}, revision: 10}
	view := &recordingView{}
	dispatcher.register(view)

	newer := app.Event{State: app.State{ProgramComplete: 12}, StateRevision: 12}
	stale := app.Event{State: app.State{ProgramComplete: 11}, StateRevision: 11}
	dispatcher.dispatch(newer)
	dispatcher.dispatch(stale)

	if got := dispatcher.state.ProgramComplete; got != 12 {
		t.Fatalf("dispatcher progress = %d, want 12", got)
	}
	if got := view.states[len(view.states)-1].ProgramComplete; got != 12 {
		t.Fatalf("view progress = %d, want 12", got)
	}
	if got := len(view.events); got != 2 {
		t.Fatalf("view received %d events, want 2", got)
	}
}

func TestViewDispatcherEqualRevisionAppliesStateOnce(t *testing.T) {
	t.Parallel()

	dispatcher := viewDispatcher{}
	view := &recordingView{}
	dispatcher.register(view)
	state := app.State{ProgramStatus: app.ProgramFailed}
	dispatcher.dispatch(app.Event{Kind: app.EventStateChanged, State: state, StateRevision: 15})
	dispatcher.dispatch(app.Event{Kind: app.EventError, State: state, StateRevision: 15})

	if got := len(view.states); got != 2 { // registration plus the accepted snapshot
		t.Fatalf("applyState called %d times, want 2", got)
	}
	if got := len(view.events); got != 2 {
		t.Fatalf("applyEvent called %d times, want 2", got)
	}
	if dispatcher.revision != 15 {
		t.Fatalf("dispatcher revision = %d, want 15", dispatcher.revision)
	}
}

func TestViewDispatcherAdvancesAfterStaleState(t *testing.T) {
	t.Parallel()

	dispatcher := viewDispatcher{}
	view := &recordingView{}
	dispatcher.register(view)
	for _, revision := range []app.StateRevision{20, 18, 21} {
		dispatcher.dispatch(app.Event{State: app.State{ProgramComplete: int(revision)}, StateRevision: revision})
	}

	got := []int{view.states[1].ProgramComplete, view.states[2].ProgramComplete}
	if !reflect.DeepEqual(got, []int{20, 21}) {
		t.Fatalf("presented progress = %v, want [20 21]", got)
	}
	if len(view.events) != 3 {
		t.Fatalf("view received %d events, want 3", len(view.events))
	}
}

func TestViewDispatcherLateRegistrationUsesNewestAcceptedRevision(t *testing.T) {
	t.Parallel()

	dispatcher := viewDispatcher{state: app.State{ProgramComplete: 5}, revision: 5}
	dispatcher.dispatch(app.Event{State: app.State{ProgramComplete: 7}, StateRevision: 7})
	dispatcher.dispatch(app.Event{State: app.State{ProgramComplete: 6}, StateRevision: 6})

	late := &recordingView{}
	dispatcher.register(late)
	if got := late.states[0].ProgramComplete; got != 7 {
		t.Fatalf("late view progress = %d, want 7", got)
	}
	if dispatcher.revision != 7 {
		t.Fatalf("dispatcher revision = %d, want 7", dispatcher.revision)
	}
}

func TestViewRegistryDeliversState(t *testing.T) {
	t.Parallel()

	var registry viewRegistry
	view := &recordingView{}
	registry.add(view)
	state := app.State{Connected: true, PortName: "test-port"}
	registry.applyState(state)
	if len(view.states) != 1 || !reflect.DeepEqual(view.states[0], state) {
		t.Fatalf("view states = %#v, want %#v", view.states, []app.State{state})
	}
}
