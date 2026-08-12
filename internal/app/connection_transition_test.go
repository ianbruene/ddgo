package app

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/transport"
)

type blockingOpenTransport struct {
	events       chan transport.Event
	openStarted  chan struct{}
	releaseOpen  chan struct{}
	startOnce    sync.Once
	mu           sync.Mutex
	openCalls    int
	closeCalls   int
	writes       []transport.Message
	openErr      error
	writeStarted chan struct{}
	releaseWrite chan struct{}
	blockWrites  bool
	writeOnce    sync.Once
	writeErr     error
}

func newBlockingOpenTransport() *blockingOpenTransport {
	return &blockingOpenTransport{events: make(chan transport.Event, 16), openStarted: make(chan struct{}), releaseOpen: make(chan struct{})}
}
func (t *blockingOpenTransport) Events() <-chan transport.Event { return t.events }
func (t *blockingOpenTransport) Open(ctx context.Context, _ transport.PortConfig) (transport.ConnectionGeneration, error) {
	t.mu.Lock()
	t.openCalls++
	t.mu.Unlock()
	t.startOnce.Do(func() { close(t.openStarted) })
	select {
	case <-t.releaseOpen:
		t.mu.Lock()
		err := t.openErr
		t.openErr = nil
		t.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return transport.ConnectionGeneration(t.openCalls), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (t *blockingOpenTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeCalls++
	return nil
}
func (t *blockingOpenTransport) Write(_ context.Context, msg transport.Message) error {
	t.mu.Lock()
	t.writes = append(t.writes, msg)
	block := t.blockWrites
	err := t.writeErr
	t.mu.Unlock()
	if block {
		t.writeOnce.Do(func() { close(t.writeStarted) })
		<-t.releaseWrite
	}
	return err
}

func TestConnectAttemptInvalidatedByDisconnectBeforeCommit(t *testing.T) {
	tr := newBlockingOpenTransport()
	c := NewController(tr, nil)
	c.statusPollInterval = time.Hour
	done := beginBlockedConnect(t, tr, c, context.Background())

	tr.events <- transport.Event{Kind: transport.EventDisconnected, Generation: 1, When: time.Now()}
	// Open has not returned its physical generation yet. The bridge records the
	// disconnect and Connect correlates it synchronously after Open returns.
	for {
		c.mu.RLock()
		_, recorded := c.pendingDisconnectedGenerations[1]
		c.mu.RUnlock()
		if recorded {
			break
		}
		runtime.Gosched()
	}
	close(tr.releaseOpen)
	if err := <-done; !errors.Is(err, ErrTransportDisconnected) {
		t.Fatalf("Connect() error = %v, want ErrTransportDisconnected", err)
	}
	if state := c.Snapshot(); state.Connected {
		t.Fatal("Connect committed an invalidated attempt")
	}
	c.mu.RLock()
	transition, attempt, poll := c.connectionTransition, c.activeConnectAttempt, c.statusPollCancel
	c.mu.RUnlock()
	if transition != connectionStable || attempt != 0 || poll != nil {
		t.Fatalf("cleanup = transition %v attempt %v poll %v", transition, attempt, poll != nil)
	}
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("retry")); err != nil {
		t.Fatalf("retry Connect() error = %v", err)
	}
}

func TestRealtimeWriteReservationBlocksConnect(t *testing.T) {
	tr := newBlockingOpenTransport()
	tr.blockWrites = true
	tr.writeStarted = make(chan struct{})
	tr.releaseWrite = make(chan struct{})
	c := NewController(tr, nil)

	writeDone := make(chan error, 1)
	go func() { writeDone <- c.Action(context.Background(), grbl.ActionHold) }()
	<-tr.writeStarted
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("blocked")); !errors.Is(err, ErrControllerIOActive) {
		t.Fatalf("Connect() error = %v, want ErrControllerIOActive", err)
	}
	if opens, _, _ := tr.counts(); opens != 0 {
		t.Fatalf("Open calls = %d, want 0", opens)
	}
	close(tr.releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatalf("Action() error = %v", err)
	}
	close(tr.releaseOpen)
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("retry")); err != nil {
		t.Fatalf("retry Connect() error = %v", err)
	}
}

func TestSoftResetReservationBlocksReplacementOwner(t *testing.T) {
	tr := newBlockingOpenTransport()
	tr.blockWrites = true
	tr.writeStarted = make(chan struct{})
	tr.releaseWrite = make(chan struct{})
	c := NewController(tr, nil)
	ownerA := newResponseSession(make(chan string, 1))
	c.mu.Lock()
	c.connectionGeneration = 1
	if err := c.acquireResponseOwnerLocked(responseOwnerManualLine, ownerA); err != nil {
		c.mu.Unlock()
		t.Fatal(err)
	}
	c.mu.Unlock()

	resetDone := make(chan error, 1)
	go func() { resetDone <- c.Action(context.Background(), grbl.ActionSoftReset) }()
	<-tr.writeStarted
	if !errors.Is(ownerA.Err(), ErrControllerReset) {
		t.Fatalf("interrupted owner error = %v, want ErrControllerReset", ownerA.Err())
	}
	if _, err := c.beginInteractiveSession(); !errors.Is(err, ErrControllerIOActive) {
		t.Fatalf("replacement owner error = %v, want ErrControllerIOActive", err)
	}
	close(tr.releaseWrite)
	if err := <-resetDone; err != nil {
		t.Fatalf("Soft Reset error = %v", err)
	}
	if session, err := c.beginInteractiveSession(); err != nil {
		t.Fatalf("replacement after reset error = %v", err)
	} else {
		c.endInteractiveSession(session, nil)
	}
}

func TestDisconnectWaitsForRealtimeWrite(t *testing.T) {
	tr := newBlockingOpenTransport()
	c := NewController(tr, nil)
	c.statusPollInterval = time.Hour
	close(tr.releaseOpen)
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("connected")); err != nil {
		t.Fatal(err)
	}
	tr.blockWrites = true
	tr.writeStarted = make(chan struct{})
	tr.releaseWrite = make(chan struct{})
	writeDone := make(chan error, 1)
	go func() { writeDone <- c.Action(context.Background(), grbl.ActionHold) }()
	<-tr.writeStarted
	disconnectDone := make(chan error, 1)
	go func() { disconnectDone <- c.Disconnect() }()
	for {
		c.mu.RLock()
		disconnecting := c.connectionTransition == connectionDisconnecting
		c.mu.RUnlock()
		if disconnecting {
			break
		}
		runtime.Gosched()
	}
	if _, closes, _ := tr.counts(); closes != 0 {
		t.Fatalf("Close calls while write blocked = %d", closes)
	}
	close(tr.releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disconnectDone; err != nil {
		t.Fatal(err)
	}
	if _, closes, _ := tr.counts(); closes != 1 {
		t.Fatalf("Close calls = %d, want 1", closes)
	}
}
func (t *blockingOpenTransport) counts() (int, int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.openCalls, t.closeCalls, len(t.writes)
}

func beginBlockedConnect(t *testing.T, tr *blockingOpenTransport, c *Controller, ctx context.Context) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx, transport.DefaultPortConfig("blocked")) }()
	select {
	case <-tr.openStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("transport Open did not start")
	}
	return done
}

func TestConnectAdmissionBlocksCommandsAndConcurrentConnect(t *testing.T) {
	tr := newBlockingOpenTransport()
	c := NewController(tr, nil)
	c.statusPollInterval = time.Hour
	if err := c.LoadProgramFile(writeProgramFile(t, "transition.gcode", "G0 X2\n")); err != nil {
		t.Fatalf("LoadProgramFile() error = %v", err)
	}
	drainEvents(c.Events())
	done := beginBlockedConnect(t, tr, c, context.Background())

	checks := []struct {
		name string
		fn   func() error
	}{
		{"interactive", func() error { _, err := c.beginInteractiveSession(); return err }},
		{"gcode", func() error { return c.SendConsoleLine(context.Background(), "G0 X1") }},
		{"macro-like-line", func() error { return c.SendConsoleLine(context.Background(), "M103") }},
		{"query", func() error { return c.SendConsoleLine(context.Background(), "$I") }},
		{"jog", func() error { return c.Jog(context.Background(), "X", 1, 10) }},
		{"jog-to", func() error { return c.JogTo(context.Background(), "X", 1, 10) }},
		{"unlock", func() error { return c.Action(context.Background(), grbl.ActionUnlock) }},
		{"home", func() error { return c.Action(context.Background(), grbl.ActionHome) }},
		{"status", func() error { return c.Action(context.Background(), grbl.ActionStatus) }},
		{"hold", func() error { return c.Action(context.Background(), grbl.ActionHold) }},
		{"resume", func() error { return c.Action(context.Background(), grbl.ActionResume) }},
		{"jog-cancel", func() error { return c.StopMotion(context.Background()) }},
		{"soft-reset", func() error { return c.Action(context.Background(), grbl.ActionSoftReset) }},
		{"program", func() error { return c.StartProgram(context.Background()) }},
		{"concurrent-connect", func() error { return c.Connect(context.Background(), transport.DefaultPortConfig("second")) }},
		{"disconnect", c.Disconnect},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.fn(); !errors.Is(err, ErrConnectionTransition) {
				t.Fatalf("error = %v, want ErrConnectionTransition", err)
			}
		})
	}
	if opens, closes, writes := tr.counts(); opens != 1 || closes != 0 || writes != 0 {
		t.Fatalf("transport counts = opens %d, closes %d, writes %d; want 1, 0, 0", opens, closes, writes)
	}
	c.mu.RLock()
	owner := c.responseOwner.kind
	c.mu.RUnlock()
	if owner != responseOwnerNone {
		t.Fatalf("response owner = %v, want none", owner)
	}
	if state := c.Snapshot(); state.ProgramStatus != ProgramLoaded || state.ProgramComplete != 0 {
		t.Fatalf("program state during Connect = %+v, want loaded and not started", state)
	}
	for draining := true; draining; {
		select {
		case ev := <-c.Events():
			if ev.Kind == EventStateChanged && ev.State.ProgramStatus == ProgramRunning {
				t.Fatalf("unexpected program-start event during rejected admission: %#v", ev)
			}
		default:
			draining = false
		}
	}
	close(tr.releaseOpen)
	if err := <-done; err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := c.StartProgram(context.Background()); err != nil {
		t.Fatalf("StartProgram() after Connect error = %v", err)
	}
	tr.events <- transport.Event{Kind: transport.EventRX, Generation: 1, When: time.Now(), Text: "ok"}
	waitForState(t, c, func(state State) bool { return state.ProgramStatus == ProgramCompleted })
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("again")); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("second stable Connect() error = %v, want ErrAlreadyConnected", err)
	}
}

func TestConnectionTransitionFailedAndCanceledConnectReleaseAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{"failure", false}, {"canceled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newBlockingOpenTransport()
			c := NewController(tr, nil)
			c.statusPollInterval = time.Hour
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := beginBlockedConnect(t, tr, c, ctx)
			want := errors.New("open failed")
			if tc.cancel {
				want = context.Canceled
				cancel()
			} else {
				tr.openErr = want
				close(tr.releaseOpen)
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("Connect() error = %v, want %v", err, want)
			}
			if tc.cancel {
				close(tr.releaseOpen)
			}
			if state := c.Snapshot(); state.Connected {
				t.Fatalf("state.Connected = true after failed Connect")
			}
			c.mu.RLock()
			transition, owner := c.connectionTransition, c.responseOwner.kind
			c.mu.RUnlock()
			if transition != connectionStable || owner != responseOwnerNone {
				t.Fatalf("transition/owner = %v/%v, want stable/none", transition, owner)
			}
			if err := c.Connect(context.Background(), transport.DefaultPortConfig("retry")); err != nil {
				t.Fatalf("retry Connect() error = %v", err)
			}
		})
	}
}
