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

	event := app.Event{Kind: app.EventStateChanged, State: next}
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

	dispatcher.dispatch(app.Event{Kind: app.EventStateChanged, State: next})
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
