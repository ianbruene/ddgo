package ui

import (
	"reflect"
	"testing"

	"github.com/ianbruene/ddgo/internal/app"
)

type fakeApplicationWindow struct {
	states  []app.State
	events  []app.Event
	onClose func()
}

func (w *fakeApplicationWindow) applyState(state app.State) { w.states = append(w.states, state) }
func (w *fakeApplicationWindow) applyEvent(event app.Event) { w.events = append(w.events, event) }
func (w *fakeApplicationWindow) setOnClose(fn func())       { w.onClose = fn }
func (w *fakeApplicationWindow) close()                     { w.onClose() }

func newTestWindowManager(state app.State) (*windowManager, *viewDispatcher) {
	dispatcher := &viewDispatcher{state: state}
	return &windowManager{dispatcher: dispatcher}, dispatcher
}

func TestWindowManagerRegistersWindowAndSuppliesInitialState(t *testing.T) {
	t.Parallel()
	initial := app.State{PortName: "initial"}
	manager, dispatcher := newTestWindowManager(initial)
	window := &fakeApplicationWindow{}
	id := manager.add(window)

	if id == 0 || manager.len() != 1 || len(dispatcher.views.views) != 1 {
		t.Fatalf("id = %d, managed = %d, registered = %d; want nonzero, 1, 1", id, manager.len(), len(dispatcher.views.views))
	}
	if !reflect.DeepEqual(window.states, []app.State{initial}) {
		t.Fatalf("initial states = %#v, want %#v", window.states, []app.State{initial})
	}
}

func TestWindowManagerSupportsMultipleWindowsAndBroadcasts(t *testing.T) {
	t.Parallel()
	manager, dispatcher := newTestWindowManager(app.State{})
	windows := []*fakeApplicationWindow{{}, {}, {}}
	ids := make(map[windowID]bool)
	for _, window := range windows {
		ids[manager.add(window)] = true
	}
	if len(ids) != len(windows) || manager.len() != len(windows) || len(dispatcher.views.views) != len(windows) {
		t.Fatalf("unique IDs = %d, managed = %d, registered = %d; want %d each", len(ids), manager.len(), len(dispatcher.views.views), len(windows))
	}

	event := app.Event{Kind: app.EventStateChanged, Text: "broadcast"}
	dispatcher.dispatch(event)
	for i, window := range windows {
		if !reflect.DeepEqual(window.events, []app.Event{event}) {
			t.Errorf("window %d events = %#v, want %#v", i, window.events, []app.Event{event})
		}
	}
}

func TestWindowManagerCloseRemovesOnlyClosedWindowAndIsIdempotent(t *testing.T) {
	t.Parallel()
	manager, dispatcher := newTestWindowManager(app.State{})
	windows := []*fakeApplicationWindow{{}, {}, {}}
	ids := make([]windowID, len(windows))
	for i, window := range windows {
		ids[i] = manager.add(window)
	}

	windows[1].close()
	windows[1].close()
	manager.remove(ids[1])
	if manager.len() != 2 || len(dispatcher.views.views) != 2 {
		t.Fatalf("managed = %d, registered = %d; want 2, 2", manager.len(), len(dispatcher.views.views))
	}
	if _, ok := manager.windows[ids[0]]; !ok {
		t.Error("first unrelated window was removed")
	}
	if _, ok := manager.windows[ids[2]]; !ok {
		t.Error("third unrelated window was removed")
	}

	event := app.Event{Kind: app.EventError, Text: "after close"}
	dispatcher.dispatch(event)
	if len(windows[1].events) != 0 {
		t.Fatalf("closed window received %d events, want 0", len(windows[1].events))
	}
	if len(windows[0].events) != 1 || len(windows[2].events) != 1 {
		t.Fatalf("survivor event counts = %d, %d; want 1, 1", len(windows[0].events), len(windows[2].events))
	}
}

func TestWindowManagerRetainsLargeArbitraryCountAndRemovesAll(t *testing.T) {
	t.Parallel()
	manager, dispatcher := newTestWindowManager(app.State{})
	const count = 100
	windows := make([]*fakeApplicationWindow, count)
	ids := make(map[windowID]struct{}, count)
	for i := range windows {
		windows[i] = &fakeApplicationWindow{}
		ids[manager.add(windows[i])] = struct{}{}
	}
	if len(ids) != count || manager.len() != count || len(dispatcher.views.views) != count {
		t.Fatalf("unique IDs = %d, managed = %d, registered = %d; want %d each", len(ids), manager.len(), len(dispatcher.views.views), count)
	}
	for _, window := range windows {
		window.close()
	}
	if manager.len() != 0 || len(dispatcher.views.views) != 0 {
		t.Fatalf("managed = %d, registered = %d after closing all; want 0, 0", manager.len(), len(dispatcher.views.views))
	}
}
