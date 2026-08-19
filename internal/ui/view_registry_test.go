package ui

import (
	"reflect"
	"testing"

	"github.com/ianbruene/ddgo/internal/app"
)

type recordingView struct {
	states []app.State
	events []app.Event
}

func (v *recordingView) applyState(state app.State) { v.states = append(v.states, state) }
func (v *recordingView) applyEvent(event app.Event) { v.events = append(v.events, event) }

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
