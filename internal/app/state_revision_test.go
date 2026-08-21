package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ianbruene/ddgo/internal/ports"
)

func TestCaptureEventStateRevisionAndSnapshotWithRevision(t *testing.T) {
	t.Parallel()

	c := &Controller{state: State{ProgramComplete: 3}}
	first := c.captureEventState()
	second := c.captureEventState()
	if second.revision <= first.revision {
		t.Fatalf("second revision = %d, want greater than %d", second.revision, first.revision)
	}

	state, revision := c.SnapshotWithRevision()
	if !reflect.DeepEqual(state, second.state) || revision != second.revision {
		t.Fatalf("SnapshotWithRevision() = (%#v, %d), want (%#v, %d)", state, revision, second.state, second.revision)
	}
	_, after := c.SnapshotWithRevision()
	if after != revision {
		t.Fatalf("SnapshotWithRevision advanced revision from %d to %d", revision, after)
	}
}

func TestStandaloneControllerEventsCarryStateRevisions(t *testing.T) {
	t.Parallel()

	c := &Controller{
		events: make(chan Event, 2),
		listPorts: func(context.Context) ([]ports.Info, error) {
			return []ports.Info{{Name: "test-port"}}, nil
		},
	}
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatalf("RefreshPorts() error = %v", err)
	}
	portEvent := <-c.events
	if portEvent.StateRevision == 0 {
		t.Fatal("ports event has no state revision")
	}

	c.emitError(errors.New("test error"))
	errorEvent := <-c.events
	if errorEvent.StateRevision <= portEvent.StateRevision {
		t.Fatalf("error revision = %d, want greater than ports revision %d", errorEvent.StateRevision, portEvent.StateRevision)
	}
}
