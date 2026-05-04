package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

func TestControllerRefreshPorts(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList([]ports.Info{{Name: "/dev/ttyACM0", IsUSB: true}}, nil))

	if err := controller.RefreshPorts(context.Background()); err != nil {
		t.Fatalf("RefreshPorts() error = %v", err)
	}

	ev := waitForEvent(t, controller.Events(), EventPortsRefreshed)
	if got, want := len(ev.Ports), 1; got != want {
		t.Fatalf("len(ev.Ports) = %d, want %d", got, want)
	}
	if got, want := ev.Ports[0].Name, "/dev/ttyACM0"; got != want {
		t.Fatalf("ev.Ports[0].Name = %q, want %q", got, want)
	}
	ev.Ports[0].Name = "mutated"

	if err := controller.RefreshPorts(context.Background()); err != nil {
		t.Fatalf("RefreshPorts() second call error = %v", err)
	}
	second := waitForEvent(t, controller.Events(), EventPortsRefreshed)
	if got, want := second.Ports[0].Name, "/dev/ttyACM0"; got != want {
		t.Fatalf("second refresh name = %q, want %q", got, want)
	}
}

func TestControllerRefreshPorts_Errors(t *testing.T) {
	t.Parallel()

	controller := NewController(transport.NewFakeTransport(), nil)
	if err := controller.RefreshPorts(context.Background()); err == nil {
		t.Fatal("RefreshPorts() error = nil, want non-nil")
	}
	ev := waitForEvent(t, controller.Events(), EventError)
	if got, want := ev.Text, "port lister is not configured"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}

	wantErr := errors.New("list failed")
	controller = NewController(transport.NewFakeTransport(), ports.StaticList(nil, wantErr))
	if err := controller.RefreshPorts(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("RefreshPorts() error = %v, want %v", err, wantErr)
	}
	ev = waitForEvent(t, controller.Events(), EventError)
	if !errors.Is(ev.Err, wantErr) {
		t.Fatalf("event error = %v, want %v", ev.Err, wantErr)
	}
}

func TestControllerConnectSendJogActionAndReceive(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	cfg := transport.DefaultPortConfig("/dev/ttyACM0")

	if err := controller.Connect(context.Background(), cfg); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	stateChanged := waitForEvent(t, controller.Events(), EventStateChanged)
	if got, want := stateChanged.Text, "connected to /dev/ttyACM0"; got != want {
		t.Fatalf("state change text = %q, want %q", got, want)
	}

	state := controller.Snapshot()
	if !state.Connected {
		t.Fatalf("connected state = false, want true")
	}
	if state.PortName != cfg.Name {
		t.Fatalf("state.PortName = %q, want %q", state.PortName, cfg.Name)
	}

	if err := controller.SendConsoleLine(context.Background(), "G0 X10"); err != nil {
		t.Fatalf("SendConsoleLine() error = %v", err)
	}
	if err := controller.Jog(context.Background(), "X", 1.5, 250); err != nil {
		t.Fatalf("Jog() error = %v", err)
	}
	if err := controller.Action(context.Background(), grbl.ActionStatus); err != nil {
		t.Fatalf("Action() error = %v", err)
	}

	for i := 0; i < 3; i++ {
		_ = waitForEvent(t, controller.Events(), EventConsoleTX)
	}

	fake.InjectRX("<Idle|MPos:0.000,0.000,0.000>")
	rx := waitForEvent(t, controller.Events(), EventConsoleRX)
	if got, want := rx.Text, "<Idle|MPos:0.000,0.000,0.000>"; got != want {
		t.Fatalf("rx text = %q, want %q", got, want)
	}

	if got, want := controller.Snapshot().MachineState, "Idle"; got != want {
		t.Fatalf("MachineState = %q, want %q", got, want)
	}

	written := fake.Written()
	if got, want := len(written), 3; got != want {
		t.Fatalf("len(written) = %d, want %d", got, want)
	}
	if got, want := string(written[0].Payload), "G0 X10\n"; got != want {
		t.Fatalf("written[0] = %q, want %q", got, want)
	}
	if got, want := written[1].Display, "$J=G91 X1.500 F250"; got != want {
		t.Fatalf("written[1].Display = %q, want %q", got, want)
	}
	if got, want := string(written[2].Payload), "?"; got != want {
		t.Fatalf("written[2] = %q, want %q", got, want)
	}
}

func TestControllerConnectValidationAndOpenError(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))

	if err := controller.Connect(context.Background(), transport.PortConfig{}); err == nil {
		t.Fatal("Connect() with empty port name error = nil, want non-nil")
	}
	ev := waitForEvent(t, controller.Events(), EventError)
	if got, want := ev.Text, "port name is required"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}

	wantErr := errors.New("open failed")
	fake.SetOpenError(wantErr)
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); !errors.Is(err, wantErr) {
		t.Fatalf("Connect() error = %v, want %v", err, wantErr)
	}
	ev = waitForEvent(t, controller.Events(), EventError)
	if !errors.Is(ev.Err, wantErr) {
		t.Fatalf("event error = %v, want %v", ev.Err, wantErr)
	}
	if controller.Snapshot().Connected {
		t.Fatal("state.Connected = true after failed connect, want false")
	}
}

func TestControllerDisconnect(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	fake.InjectRX("<Run|MPos:0.000,0.000,0.000>")
	_ = waitForEvent(t, controller.Events(), EventConsoleRX)

	if err := controller.Disconnect(); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	ev := waitForEvent(t, controller.Events(), EventStateChanged)
	if got, want := ev.Text, "disconnected"; got != want {
		t.Fatalf("state change text = %q, want %q", got, want)
	}
	state := controller.Snapshot()
	if state.Connected {
		t.Fatal("state.Connected = true, want false")
	}
	if got := state.MachineState; got != "" {
		t.Fatalf("state.MachineState = %q, want empty", got)
	}
}

func TestControllerDisconnectError(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	wantErr := errors.New("close failed")
	fake.SetCloseError(wantErr)
	if err := controller.Disconnect(); !errors.Is(err, wantErr) {
		t.Fatalf("Disconnect() error = %v, want %v", err, wantErr)
	}
	ev := waitForEvent(t, controller.Events(), EventError)
	if !errors.Is(ev.Err, wantErr) {
		t.Fatalf("event error = %v, want %v", ev.Err, wantErr)
	}
	if !controller.Snapshot().Connected {
		t.Fatal("state.Connected = false after failed disconnect, want true")
	}
}

func TestControllerPropagatesWriteAndBuildErrors(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	wantErr := errors.New("write failed")
	fake.SetWriteError(wantErr)
	if err := controller.SendConsoleLine(context.Background(), "G0 X1"); !errors.Is(err, wantErr) {
		t.Fatalf("SendConsoleLine() error = %v, want %v", err, wantErr)
	}
	ev := waitForEvent(t, controller.Events(), EventError)
	if !errors.Is(ev.Err, wantErr) {
		t.Fatalf("event error = %v, want %v", ev.Err, wantErr)
	}
	if got, want := controller.Snapshot().LastError, wantErr.Error(); got != want {
		t.Fatalf("LastError = %q, want %q", got, want)
	}

	fake.SetWriteError(nil)
	if err := controller.Jog(context.Background(), "A", 1, 100); err == nil {
		t.Fatal("Jog() invalid axis error = nil, want non-nil")
	}
	ev = waitForEvent(t, controller.Events(), EventError)
	if got, want := ev.Text, "unsupported jog axis \"A\""; got != want {
		t.Fatalf("build error text = %q, want %q", got, want)
	}

	if err := controller.Action(context.Background(), grbl.Action("bad")); err == nil {
		t.Fatal("Action() invalid action error = nil, want non-nil")
	}
	ev = waitForEvent(t, controller.Events(), EventError)
	if got, want := ev.Text, "unsupported action \"bad\""; got != want {
		t.Fatalf("build error text = %q, want %q", got, want)
	}
}

func TestControllerTransportErrorAndRXHandling(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	fake.InjectRX("garbage")
	ev := waitForEvent(t, controller.Events(), EventConsoleRX)
	if got, want := ev.State.MachineState, ""; got != want {
		t.Fatalf("rx state machine = %q, want %q", got, want)
	}

	transportErr := errors.New("device fault")
	fake.InjectError(transportErr)
	errEv := waitForEvent(t, controller.Events(), EventError)
	if !errors.Is(errEv.Err, transportErr) {
		t.Fatalf("event error = %v, want %v", errEv.Err, transportErr)
	}
	if got, want := controller.Snapshot().LastError, transportErr.Error(); got != want {
		t.Fatalf("LastError = %q, want %q", got, want)
	}
}

func waitForEvent(t *testing.T, ch <-chan Event, kind EventKind) Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event kind %q", kind)
		}
	}
}

func TestControllerProgramLoadStartComplete(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "demo.gcode", "(comment)\nG21\nG0 X1\nG0 Y2 ; move\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	if err := controller.LoadProgramFile(path); err != nil {
		t.Fatalf("LoadProgramFile() error = %v", err)
	}
	loadEv := waitForEvent(t, controller.Events(), EventStateChanged)
	if got, want := loadEv.State.ProgramStatus, ProgramLoaded; got != want {
		t.Fatalf("program status = %q, want %q", got, want)
	}
	if got, want := loadEv.State.ProgramTotal, 3; got != want {
		t.Fatalf("ProgramTotal = %d, want %d", got, want)
	}

	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	if err := controller.StartProgram(context.Background()); err != nil {
		t.Fatalf("StartProgram() error = %v", err)
	}
	startEv := waitForEvent(t, controller.Events(), EventStateChanged)
	if got, want := startEv.State.ProgramStatus, ProgramRunning; got != want {
		t.Fatalf("start status = %q, want %q", got, want)
	}

	waitForWrites(t, fake, 1)
	written := fake.Written()
	if got, want := written[0].Display, "G21"; got != want {
		t.Fatalf("first written display = %q, want %q", got, want)
	}

	fake.InjectRX("<Run|MPos:0.000,0.000,0.000>")
	rx := waitForEvent(t, controller.Events(), EventConsoleRX)
	if got, want := rx.State.MachineState, "Run"; got != want {
		t.Fatalf("machine state = %q, want %q", got, want)
	}
	if got, want := rx.State.ProgramComplete, 0; got != want {
		t.Fatalf("ProgramComplete after status rx = %d, want %d", got, want)
	}

	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramRunning })
	written = fake.Written()
	if got, want := written[1].Display, "G0 X1"; got != want {
		t.Fatalf("second written display = %q, want %q", got, want)
	}

	fake.InjectRX("ok")
	waitForWrites(t, fake, 3)
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 2 && s.ProgramStatus == ProgramRunning })
	written = fake.Written()
	if got, want := written[2].Display, "G0 Y2"; got != want {
		t.Fatalf("third written display = %q, want %q", got, want)
	}

	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 3 && s.ProgramStatus == ProgramCompleted })
	completeEv := waitForEventText(t, controller.Events(), EventStateChanged, "program demo.gcode completed")
	if got, want := completeEv.State.ProgramStatus, ProgramCompleted; got != want {
		t.Fatalf("complete status = %q, want %q", got, want)
	}
}

func TestControllerProgramPauseResumeStopAndModalBlocking(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "job.gcode", "G0 X1\nG0 X2\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	if err := controller.LoadProgramFile(path); err != nil {
		t.Fatalf("LoadProgramFile() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	if err := controller.StartProgram(context.Background()); err != nil {
		t.Fatalf("StartProgram() error = %v", err)
	}
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program job.gcode")
	waitForWrites(t, fake, 1)

	if err := controller.Jog(context.Background(), "X", 1, 100); !errors.Is(err, ErrProgramActive) {
		t.Fatalf("Jog() during program error = %v, want %v", err, ErrProgramActive)
	}
	_ = waitForEvent(t, controller.Events(), EventError)

	if err := controller.PauseProgram(context.Background()); err != nil {
		t.Fatalf("PauseProgram() error = %v", err)
	}
	pauseEv := waitForEventText(t, controller.Events(), EventStateChanged, "program paused")
	if got, want := pauseEv.State.ProgramStatus, ProgramPaused; got != want {
		t.Fatalf("pause status = %q, want %q", got, want)
	}
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "!"; got != want {
		t.Fatalf("pause write = %q, want %q", got, want)
	}

	fake.InjectRX("ok")
	ensureWritesStayAt(t, fake, 2, 150*time.Millisecond)

	if err := controller.ResumeProgram(context.Background()); err != nil {
		t.Fatalf("ResumeProgram() error = %v", err)
	}
	resumeEv := waitForEventText(t, controller.Events(), EventStateChanged, "program resumed")
	if got, want := resumeEv.State.ProgramStatus, ProgramRunning; got != want {
		t.Fatalf("resume status = %q, want %q", got, want)
	}
	waitForWrites(t, fake, 4)
	written := fake.Written()
	if got, want := written[2].Display, "~"; got != want {
		t.Fatalf("resume write = %q, want %q", got, want)
	}
	if got, want := written[3].Display, "G0 X2"; got != want {
		t.Fatalf("next program line = %q, want %q", got, want)
	}

	if err := controller.StopProgram(context.Background()); err != nil {
		t.Fatalf("StopProgram() error = %v", err)
	}
	stopEv := waitForEventText(t, controller.Events(), EventStateChanged, "program stopped")
	if got, want := stopEv.State.ProgramStatus, ProgramStopped; got != want {
		t.Fatalf("stop status = %q, want %q", got, want)
	}
	waitForWrites(t, fake, 6)
	written = fake.Written()
	if got, want := written[4].Display, "!"; got != want {
		t.Fatalf("stop hold write = %q, want %q", got, want)
	}
	if got, want := written[5].Display, "Ctrl-X"; got != want {
		t.Fatalf("stop reset write = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	ensureWritesStayAt(t, fake, 6, 150*time.Millisecond)
}

func TestControllerProgramFailureAndStartValidation(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	if err := controller.StartProgram(context.Background()); err == nil {
		t.Fatal("StartProgram() without load/connect error = nil, want non-nil")
	}
	_ = waitForEvent(t, controller.Events(), EventError)

	path := writeProgramFile(t, "bad.gcode", "G0 X1\n")
	if err := controller.LoadProgramFile(path); err != nil {
		t.Fatalf("LoadProgramFile() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	if err := controller.StartProgram(context.Background()); err == nil {
		t.Fatal("StartProgram() while disconnected error = nil, want non-nil")
	}
	_ = waitForEvent(t, controller.Events(), EventError)

	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	if err := controller.StartProgram(context.Background()); err != nil {
		t.Fatalf("StartProgram() error = %v", err)
	}
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program bad.gcode")
	waitForWrites(t, fake, 1)

	fake.InjectRX("error:2")
	waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	errEv := waitForEvent(t, controller.Events(), EventError)
	if got, want := errEv.Text, "program failed at line 1: error:2"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func writeProgramFile(t *testing.T, name string, contents string) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func waitForWrites(t *testing.T, fake *transport.FakeTransport, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := len(fake.Written()); got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d writes; got %d", want, len(fake.Written()))
}

func ensureWritesStayAt(t *testing.T, fake *transport.FakeTransport, want int, dur time.Duration) {
	t.Helper()
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		if got := len(fake.Written()); got != want {
			t.Fatalf("writes changed to %d, want %d", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForState(t *testing.T, controller *Controller, fn func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := controller.Snapshot()
		if fn(state) {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	state := controller.Snapshot()
	t.Fatalf("timed out waiting for state; last state = %+v", state)
	return State{}
}

func waitForEventText(t *testing.T, ch <-chan Event, kind EventKind, text string) Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind && ev.Text == text {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event kind %q with text %q", kind, text)
		}
	}
}
