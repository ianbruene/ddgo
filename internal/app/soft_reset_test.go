package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

func newConnectedResetController(t *testing.T) (*Controller, *transport.FakeTransport) {
	t.Helper()
	fake := transport.NewFakeTransport()
	c := NewController(fake, ports.StaticList(nil, nil))
	c.statusPollInterval = time.Hour
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("fake")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	drainEvents(c.Events())
	return c, fake
}

func assertOwner(t *testing.T, c *Controller, want responseOwnerKind) responseOwner {
	t.Helper()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.responseOwner.kind != want {
		t.Fatalf("response owner = %v, want %v", c.responseOwner.kind, want)
	}
	return c.responseOwner
}

func TestControllerSoftResetWithNoOwnerAndManualOwner(t *testing.T) {
	t.Run("no owner", func(t *testing.T) {
		c, fake := newConnectedResetController(t)
		if err := c.Action(context.Background(), grbl.ActionSoftReset); err != nil {
			t.Fatalf("Action(SoftReset) error = %v", err)
		}
		assertOwner(t, c, responseOwnerNone)
		if got := fake.Written()[0].Payload; len(got) != 1 || got[0] != 0x18 {
			t.Fatalf("reset payload = %v, want Ctrl-X", got)
		}
		if err := c.SendConsoleLine(context.Background(), "G0 X2"); err != nil {
			t.Fatalf("later manual command error = %v", err)
		}
		assertOwner(t, c, responseOwnerManualLine)
	})

	t.Run("pending manual", func(t *testing.T) {
		c, fake := newConnectedResetController(t)
		if err := c.SendConsoleLine(context.Background(), "G0 X1"); err != nil {
			t.Fatalf("first manual command error = %v", err)
		}
		assertOwner(t, c, responseOwnerManualLine)
		if err := c.Action(context.Background(), grbl.ActionSoftReset); err != nil {
			t.Fatalf("Action(SoftReset) error = %v", err)
		}
		assertOwner(t, c, responseOwnerNone)
		if got := fake.Written()[1].Payload; len(got) != 1 || got[0] != 0x18 {
			t.Fatalf("reset payload = %v, want Ctrl-X", got)
		}
		if err := c.SendConsoleLine(context.Background(), "G0 X2"); err != nil {
			t.Fatalf("later manual command error = %v", err)
		}
	})
}

func TestControllerSoftResetCancelsInteractiveCommands(t *testing.T) {
	tests := []struct {
		name, command, write string
		probe                bool
	}{
		{"wait for OK", "M102 G54Z = 4", "G10 L2 P1 Z4.000000", false},
		{"query", "M101 G54Z G55Z 0.1", "$#", false},
		{"M109 probe", "M109 G38.2 Z-5 F100", "G38.2 Z-5 F100", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, fake := newConnectedResetController(t)
			done := make(chan error, 1)
			go func() { done <- c.SendConsoleLine(context.Background(), tt.command) }()
			waitForWrites(t, fake, 1)
			if got := fake.Written()[0].Display; got != tt.write {
				t.Fatalf("command write = %q, want %q", got, tt.write)
			}
			owner := assertOwner(t, c, responseOwnerInteractiveMacro)
			if err := c.Action(context.Background(), grbl.ActionSoftReset); err != nil {
				t.Fatalf("Action(SoftReset) error = %v", err)
			}
			if err := waitForErrorResult(t, done); !errors.Is(err, ErrControllerReset) {
				t.Fatalf("interactive error = %v, want %v", err, ErrControllerReset)
			}
			assertOwner(t, c, responseOwnerNone)
			if owner.session.queryRxCh != nil {
				t.Fatal("query collector remains installed")
			}
			if tt.probe && len(c.Contour().Points()) != 0 {
				t.Fatalf("contour points = %v, want none", c.Contour().Points())
			}
			if err := c.SendConsoleLine(context.Background(), "G0 X2"); err != nil {
				t.Fatalf("later command error = %v", err)
			}
		})
	}
}

func TestControllerSoftResetRejectedForProgramOwner(t *testing.T) {
	c, fake := newConnectedResetController(t)
	path := writeProgramFile(t, "reset-program.gcode", "G0 X1\n")
	if err := c.LoadProgramFile(path); err != nil {
		t.Fatal(err)
	}
	if err := c.StartProgram(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForWrites(t, fake, 1)
	owner := assertOwner(t, c, responseOwnerProgram)
	if err := c.Action(context.Background(), grbl.ActionSoftReset); !errors.Is(err, ErrProgramActive) {
		t.Fatalf("Action(SoftReset) error = %v, want %v", err, ErrProgramActive)
	}
	if len(fake.Written()) != 1 {
		t.Fatalf("writes = %#v, want no reset write", fake.Written())
	}
	if got := assertOwner(t, c, responseOwnerProgram); got != owner {
		t.Fatal("program owner changed")
	}
	fake.InjectRX("ok")
	waitForState(t, c, func(s State) bool { return s.ProgramStatus == ProgramCompleted })
}

func TestControllerSoftResetWriteFailureLeavesInteractiveCanceled(t *testing.T) {
	c, fake := newConnectedResetController(t)
	done := make(chan error, 1)
	go func() { done <- c.SendConsoleLine(context.Background(), "M102 G54Z = 4") }()
	waitForWrites(t, fake, 1)
	want := errors.New("reset write failed")
	fake.SetWriteError(want)
	if err := c.Action(context.Background(), grbl.ActionSoftReset); !errors.Is(err, want) {
		t.Fatalf("Action(SoftReset) error = %v, want %v", err, want)
	}
	if err := waitForErrorResult(t, done); !errors.Is(err, ErrControllerReset) {
		t.Fatalf("interactive error = %v, want %v", err, ErrControllerReset)
	}
	assertOwner(t, c, responseOwnerNone)
}

func TestControllerResetCanceledSessionCleanupIsIdentitySafe(t *testing.T) {
	c, _ := newConnectedResetController(t)
	a, err := c.beginInteractiveSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := func() error { c.mu.Lock(); defer c.mu.Unlock(); return c.prepareSoftResetLocked() }(); err != nil {
		t.Fatal(err)
	}
	b, err := c.beginInteractiveSession()
	if err != nil {
		t.Fatal(err)
	}
	c.endInteractiveSession(a, nil)
	owner := assertOwner(t, c, responseOwnerInteractiveMacro)
	if owner.session != b {
		t.Fatal("deferred cleanup for owner A cleared owner B")
	}
}
