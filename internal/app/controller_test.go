package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/gcode"
	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/macro"
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
	controller.statusPollInterval = 10 * time.Second
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

func TestControllerJogToWritesMachineJog(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	if err := controller.JogTo(context.Background(), "x", -300, 500); err != nil {
		t.Fatalf("JogTo() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventConsoleTX)

	written := fake.Written()
	if got, want := len(written), 1; got != want {
		t.Fatalf("len(written) = %d, want %d", got, want)
	}
	if got, want := written[0].Display, "$J=G53 G90 X-300.000 F500"; got != want {
		t.Fatalf("written[0].Display = %q, want %q", got, want)
	}
	if got, want := string(written[0].Payload), "$J=G53 G90 X-300.000 F500\n"; got != want {
		t.Fatalf("written[0].Payload = %q, want %q", got, want)
	}
}

func TestControllerStopMotionWritesJogCancel(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	if err := controller.StopMotion(context.Background()); err != nil {
		t.Fatalf("StopMotion() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventConsoleTX)

	written := fake.Written()
	if got, want := len(written), 1; got != want {
		t.Fatalf("len(written) = %d, want %d", got, want)
	}
	if got, want := written[0].Display, "Jog Cancel"; got != want {
		t.Fatalf("written[0].Display = %q, want %q", got, want)
	}
	if got, want := string(written[0].Payload), string([]byte{0x85}); got != want {
		t.Fatalf("written[0].Payload = %q, want %q", got, want)
	}
}

func TestControllerJogToAndStopMotionBlockedWhileProgramActive(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	controller.mu.Lock()
	controller.state.ProgramStatus = ProgramRunning
	controller.mu.Unlock()

	if err := controller.JogTo(context.Background(), "X", -300, 500); !errors.Is(err, ErrProgramActive) {
		t.Fatalf("JogTo() error = %v, want %v", err, ErrProgramActive)
	}
	_ = waitForEvent(t, controller.Events(), EventError)
	if err := controller.StopMotion(context.Background()); !errors.Is(err, ErrProgramActive) {
		t.Fatalf("StopMotion() error = %v, want %v", err, ErrProgramActive)
	}
	_ = waitForEvent(t, controller.Events(), EventError)

	if got := len(fake.Written()); got != 0 {
		t.Fatalf("len(written) = %d, want 0", got)
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
	controller.statusPollInterval = 10 * time.Second
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
	controller.statusPollInterval = 10 * time.Second
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

func TestControllerStatusPollingStartsAndStops(t *testing.T) {
	t.Parallel()
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 20 * time.Millisecond

	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	waitForStatusQueryWrite(t, fake, 500*time.Millisecond)

	if err := controller.Disconnect(); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	before := countStatusWrites(fake.Written())
	time.Sleep(120 * time.Millisecond)
	after := countStatusWrites(fake.Written())
	if after != before {
		t.Fatalf("status writes after disconnect changed from %d to %d", before, after)
	}
}

func TestControllerStatusReportUpdatesState(t *testing.T) {
	t.Parallel()
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second

	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	line := "<Run|MPos:1.000,2.000,3.000|WPos:4.000,5.000,6.000|FS:500,12000>"
	fake.InjectRX(line)
	_ = waitForEvent(t, controller.Events(), EventConsoleRX)

	state := controller.Snapshot()
	if state.MachineState != "Run" || !state.HasMachinePosition || !state.HasWorkPosition || !state.HasFeedSpindle {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.LastStatusRaw != line {
		t.Fatalf("LastStatusRaw = %q, want %q", state.LastStatusRaw, line)
	}
}

func waitForStatusQueryWrite(t *testing.T, fake *transport.FakeTransport, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if countStatusWrites(fake.Written()) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for status query write")
}

func countStatusWrites(writes []transport.Message) int {
	count := 0
	for _, write := range writes {
		if string(write.Payload) == "?" {
			count++
		}
	}
	return count
}

func countDisplayWrites(writes []transport.Message, display string) int {
	count := 0
	for _, write := range writes {
		if write.Display == display {
			count++
		}
	}
	return count
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
	controller.statusPollInterval = 10 * time.Second
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

func TestControllerStartProgramDisablesContourPreservesPoints(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "contour-start.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	wantPoints := enableTestContour(t, controller)
	if !controller.Contour().Enabled() {
		t.Fatal("contour enabled before start = false, want true")
	}
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program contour-start.gcode")
	if controller.Contour().Enabled() {
		t.Fatal("contour enabled after successful start = true, want false")
	}
	if got := controller.Contour().Points(); !reflect.DeepEqual(got, wantPoints) {
		t.Fatalf("contour points after start = %+v, want %+v", got, wantPoints)
	}
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "G0 X1"; got != want {
		t.Fatalf("first written display = %q, want %q", got, want)
	}
}

func TestControllerFailedStartDoesNotDisableContour(t *testing.T) {
	t.Parallel()

	controller := NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))
	wantPoints := enableTestContour(t, controller)
	if err := controller.StartProgram(context.Background()); err == nil {
		t.Fatal("StartProgram() without load/connect error = nil, want non-nil")
	}
	_ = waitForEvent(t, controller.Events(), EventError)
	if !controller.Contour().Enabled() {
		t.Fatal("contour enabled after failed start = false, want true")
	}
	if got := controller.Contour().Points(); !reflect.DeepEqual(got, wantPoints) {
		t.Fatalf("contour points after failed start = %+v, want %+v", got, wantPoints)
	}
}

func TestControllerProgramFailureDisablesContourPreservesPoints(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "contour-failure.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	wantPoints := enableTestContour(t, controller)
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program contour-failure.gcode")
	waitForWrites(t, fake, 1)
	if err := controller.Contour().Enable(); err != nil {
		t.Fatalf("Contour().Enable() after start error = %v", err)
	}

	fake.InjectRX("error:2")
	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if controller.Contour().Enabled() {
		t.Fatal("contour enabled after program failure = true, want false")
	}
	if got := controller.Contour().Points(); !reflect.DeepEqual(got, wantPoints) {
		t.Fatalf("contour points after program failure = %+v, want %+v", got, wantPoints)
	}
	if got, want := state.LastError, "program failed at line 1: error:2"; got != want {
		t.Fatalf("LastError = %q, want %q", got, want)
	}
	errEv := waitForEvent(t, controller.Events(), EventError)
	if got, want := errEv.Text, "program failed at line 1: error:2"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestControllerProgramPauseResumeStopAndModalBlocking(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "job.gcode", "G0 X1\nG0 X2\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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

func TestControllerStartProgramCanceledContext(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "canceled.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	if err := controller.LoadProgramFile(path); err != nil {
		t.Fatalf("LoadProgramFile() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := controller.StartProgram(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("StartProgram() error = %v, want %v", err, context.Canceled)
	}
	_ = waitForEvent(t, controller.Events(), EventError)
	if got, want := controller.Snapshot().ProgramStatus, ProgramLoaded; got != want {
		t.Fatalf("ProgramStatus = %q, want %q", got, want)
	}
	ensureWritesStayAt(t, fake, 0, 100*time.Millisecond)
}

func TestControllerPauseResumeWriteFailureKeepsState(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "pause-fail.gcode", "G0 X1\nG0 X2\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program pause-fail.gcode")
	waitForWrites(t, fake, 1)

	wantErr := errors.New("hold failed")
	fake.SetWriteError(wantErr)
	if err := controller.PauseProgram(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("PauseProgram() error = %v, want %v", err, wantErr)
	}
	_ = waitForEvent(t, controller.Events(), EventError)
	if got, want := controller.Snapshot().ProgramStatus, ProgramRunning; got != want {
		t.Fatalf("ProgramStatus after pause failure = %q, want %q", got, want)
	}

	fake.SetWriteError(nil)
	if err := controller.PauseProgram(context.Background()); err != nil {
		t.Fatalf("PauseProgram() retry error = %v", err)
	}
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "program paused")

	wantErr = errors.New("resume failed")
	fake.SetWriteError(wantErr)
	if err := controller.ResumeProgram(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("ResumeProgram() error = %v, want %v", err, wantErr)
	}
	_ = waitForEvent(t, controller.Events(), EventError)
	if got, want := controller.Snapshot().ProgramStatus, ProgramPaused; got != want {
		t.Fatalf("ProgramStatus after resume failure = %q, want %q", got, want)
	}
}

func TestControllerProgramResponseBacklogOverflowFailsProgram(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	run := &programRun{rxCh: make(chan string, 1)}
	controller.mu.Lock()
	controller.run = run
	controller.state.ProgramStatus = ProgramRunning
	controller.mu.Unlock()
	run.rxCh <- "ok"

	controller.mu.Lock()
	overflowRun := controller.deliverProgramResponseLocked("ok")
	controller.mu.Unlock()
	if overflowRun != run {
		t.Fatalf("deliverProgramResponseLocked() overflow run mismatch")
	}

	controller.finishProgramFailure(overflowRun, errors.New("program response backlog full"))
	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if got, want := state.LastError, "program response backlog full"; got != want {
		t.Fatalf("LastError = %q, want %q", got, want)
	}
	errEv := waitForEvent(t, controller.Events(), EventError)
	if got, want := errEv.Text, "program response backlog full"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestControllerQueryResponseBacklogOverflowFailsProgram(t *testing.T) {
	t.Parallel()

	controller := NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))
	run := &programRun{rxCh: make(chan string, 1), queryRxCh: make(chan string, 1)}
	controller.mu.Lock()
	controller.run = run
	controller.state.ProgramStatus = ProgramRunning
	controller.mu.Unlock()
	run.queryRxCh <- "[G54:1.000,2.000,3.000]"

	controller.mu.Lock()
	overflowRun := controller.deliverProgramResponseLocked("[G55:4.000,5.000,6.000]")
	controller.mu.Unlock()
	if overflowRun != run {
		t.Fatalf("deliverProgramResponseLocked() overflow run mismatch")
	}

	controller.finishProgramFailure(overflowRun, errors.New("program response backlog full"))
	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if got, want := state.LastError, "program response backlog full"; got != want {
		t.Fatalf("LastError = %q, want %q", got, want)
	}
}

func enableTestContour(t *testing.T, controller *Controller) []macro.Point {
	t.Helper()
	points := []macro.Point{
		{X: 0, Y: 0, Z: 0},
		{X: 1, Y: 0, Z: 0.1},
		{X: 0, Y: 1, Z: 0.2},
	}
	for _, point := range points {
		if err := controller.Contour().AddPoint(point); err != nil {
			t.Fatalf("Contour().AddPoint(%+v) error = %v", point, err)
		}
	}
	if err := controller.Contour().Enable(); err != nil {
		t.Fatalf("Contour().Enable() error = %v", err)
	}
	return points
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

func ensureNoEvent(t *testing.T, ch <-chan Event, dur time.Duration) {
	t.Helper()
	timer := time.NewTimer(dur)
	defer timer.Stop()

	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %+v", ev)
	case <-timer.C:
	}
}

func ensureNoErrorEvent(t *testing.T, ch <-chan Event, dur time.Duration) {
	t.Helper()
	deadline := time.After(dur)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EventError {
				t.Fatalf("unexpected error event: %+v", ev)
			}
		case <-deadline:
			return
		}
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

type testRewriter struct {
	line string
	err  error
}

func (r testRewriter) RewriteMotion(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
	if r.err != nil {
		return "", false, r.err
	}
	return r.line, true, nil
}

func TestControllerDefaultMacrosStoreNumericAndWriteWCS(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "default-macros-numeric.gcode", "M107 depth -1.25\nM108 depth G54Z\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program default-macros-numeric.gcode")
	waitForWrites(t, fake, 1)
	written := fake.Written()
	if got, want := len(written), 1; got != want {
		t.Fatalf("len(written) = %d, want %d", got, want)
	}
	if got, want := written[0].Display, "G10 L2 P1 Z-1.250000"; got != want {
		t.Fatalf("written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 2 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerDefaultMacrosReadWCSAndWriteWCS(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "default-macros-wcs.gcode", "M107 depth G54Z\nM108 depth G55Z\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program default-macros-wcs.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("first written line = %q, want %q", got, want)
	}
	fake.InjectRX("[G54:1.000,2.000,-3.500]")
	fake.InjectRX("[G55:0.000,0.000,0.000]")
	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "G10 L2 P2 Z-3.500000"; got != want {
		t.Fatalf("second written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 2 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerDefaultM100WritesMidpointAndVerifies(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "default-m100-midpoint.gcode", "M100 G54X G55X G56X\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program default-m100-midpoint.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("first written line = %q, want %q", got, want)
	}
	fake.InjectRX("[G54:1.000,0.000,0.000]")
	fake.InjectRX("[G55:3.000,0.000,0.000]")
	fake.InjectRX("[G56:0.000,0.000,0.000]")
	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "G10 L2 P3 X2.000000"; got != want {
		t.Fatalf("second written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForWrites(t, fake, 3)
	if got, want := fake.Written()[2].Display, "$#"; got != want {
		t.Fatalf("third written line = %q, want %q", got, want)
	}
	fake.InjectRX("[G56:2.000,0.000,0.000]")
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
	for _, written := range fake.Written() {
		if strings.HasPrefix(written.Display, "M100") {
			t.Fatalf("raw M100 was sent: %#v", fake.Written())
		}
	}
}

func TestControllerDefaultM100VerificationFailureFailsProgram(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "default-m100-verify-fail.gcode", "M100 G54X G55X G56X\nG0 X9\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program default-m100-verify-fail.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("first written line = %q, want %q", got, want)
	}
	fake.InjectRX("[G54:1.000,0.000,0.000]")
	fake.InjectRX("[G55:3.000,0.000,0.000]")
	fake.InjectRX("[G56:0.000,0.000,0.000]")
	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "G10 L2 P3 X2.000000"; got != want {
		t.Fatalf("second written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForWrites(t, fake, 3)
	if got, want := fake.Written()[2].Display, "$#"; got != want {
		t.Fatalf("third written line = %q, want %q", got, want)
	}
	fake.InjectRX("[G56:2.100,0.000,0.000]")
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool {
		return s.ProgramStatus == ProgramFailed && strings.Contains(s.LastError, "macro M100 failed at line 1") && strings.Contains(s.LastError, "M100 verification failed")
	})
	for _, written := range fake.Written() {
		if written.Display == "G0 X9" || strings.HasPrefix(written.Display, "M100") {
			t.Fatalf("unexpected write after failed macro: %#v", fake.Written())
		}
	}
}

func TestControllerDefaultM101PassAllowsNextLine(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "default-m101-pass.gcode", "M101 G54X G55X 0.010\nG0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program default-m101-pass.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("first written line = %q, want %q", got, want)
	}
	fake.InjectRX("[G54:1.000,0.000,0.000]")
	fake.InjectRX("[G55:1.005,0.000,0.000]")
	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "G0 X1"; got != want {
		t.Fatalf("second written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 2 && s.ProgramStatus == ProgramCompleted })
	for _, written := range fake.Written() {
		if strings.HasPrefix(written.Display, "M101") {
			t.Fatalf("raw M101 was sent: %#v", fake.Written())
		}
	}
}

func TestControllerDefaultM101FailurePreventsNextLine(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "default-m101-fail.gcode", "M101 G54X G55X 0.001\nG0 X9\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program default-m101-fail.gcode")
	waitForWrites(t, fake, 1)
	fake.InjectRX("[G54:1.000,0.000,0.000]")
	fake.InjectRX("[G55:1.010,0.000,0.000]")
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool {
		return s.ProgramStatus == ProgramFailed && strings.Contains(s.LastError, "macro M101 failed at line 1") && strings.Contains(s.LastError, "WCS comparison failed")
	})
	for _, written := range fake.Written() {
		if written.Display == "G0 X9" || strings.HasPrefix(written.Display, "M101") {
			t.Fatalf("unexpected write after failed macro: %#v", fake.Written())
		}
	}
}

func TestControllerUnregisteredMCodePassesThrough(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "unregistered-m.gcode", "M42 P1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	controller.SetMacroEngine(macro.NewEngine(macro.NewRegistry()))
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program unregistered-m.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "M42 P1"; got != want {
		t.Fatalf("written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerRegisteredMacroInterceptsAndAdvances(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "registered-m.gcode", "M42 P1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	reg := macro.NewRegistry()
	called := false
	reg.Register(42, macro.HandlerFunc(func(ctx context.Context, runtime macro.Runtime, inv macro.Invocation) error {
		called = true
		if inv.RawArgs != "P1" || inv.CleanArgs != "P1" {
			t.Fatalf("args = %q/%q", inv.RawArgs, inv.CleanArgs)
		}
		return nil
	}))
	controller.SetMacroEngine(macro.NewEngine(reg))
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program registered-m.gcode")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
	if !called {
		t.Fatal("handler was not called")
	}
	if got := len(fake.Written()); got != 0 {
		t.Fatalf("len(written) = %d, want 0", got)
	}
}

func TestControllerMacroFailureMarksProgramFailed(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "macro-fail.gcode", "M77\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	reg := macro.NewRegistry()
	reg.Register(77, macro.HandlerFunc(func(context.Context, macro.Runtime, macro.Invocation) error {
		return errors.New("macro condition failed")
	}))
	controller.SetMacroEngine(macro.NewEngine(reg))
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program macro-fail.gcode")
	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if want := "macro M77 failed at line 1: macro condition failed"; state.LastError != want {
		t.Fatalf("LastError = %q, want %q", state.LastError, want)
	}
	errEv := waitForEvent(t, controller.Events(), EventError)
	if errEv.Text != state.LastError {
		t.Fatalf("error event text = %q, want %q", errEv.Text, state.LastError)
	}
}

func TestControllerMacroHandlerCanSendControllerLines(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "macro-send.gcode", "M88\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	reg := macro.NewRegistry()
	reg.Register(88, macro.HandlerFunc(func(ctx context.Context, runtime macro.Runtime, inv macro.Invocation) error {
		if err := runtime.SendLineAndWaitOK(ctx, "G0 X1"); err != nil {
			return err
		}
		return runtime.SendLineAndWaitOK(ctx, "G0 Y2")
	}))
	controller.SetMacroEngine(macro.NewEngine(reg))
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program macro-send.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "G0 X1"; got != want {
		t.Fatalf("first write = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "G0 Y2"; got != want {
		t.Fatalf("second write = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerMacroQueryCollectsIntermediateResponses(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "macro-query.gcode", "M90\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	reg := macro.NewRegistry()
	received := make(chan []string, 1)
	reg.Register(90, macro.HandlerFunc(func(ctx context.Context, runtime macro.Runtime, inv macro.Invocation) error {
		lines, err := runtime.SendLineCollectingResponses(ctx, "$#")
		if err != nil {
			return err
		}
		received <- lines
		return nil
	}))
	controller.SetMacroEngine(macro.NewEngine(reg))
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program macro-query.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("query write = %q, want %q", got, want)
	}

	fake.InjectRX("[G54:1.000,2.000,3.000]")
	fake.InjectRX("[G55:4.000,5.000,6.000]")
	fake.InjectRX("ok")

	var lines []string
	select {
	case lines = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for collected query lines")
	}
	want := []string{"[G54:1.000,2.000,3.000]", "[G55:4.000,5.000,6.000]"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("collected lines = %#v, want %#v", lines, want)
	}
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerMacroQueryFailsOnControllerError(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "macro-query-error.gcode", "M91\nG0 X9\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	reg := macro.NewRegistry()
	reg.Register(91, macro.HandlerFunc(func(ctx context.Context, runtime macro.Runtime, inv macro.Invocation) error {
		_, err := runtime.SendLineCollectingResponses(ctx, "$#")
		return err
	}))
	controller.SetMacroEngine(macro.NewEngine(reg))
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program macro-query-error.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("query write = %q, want %q", got, want)
	}

	fake.InjectRX("[G54:1.000,2.000,3.000]")
	fake.InjectRX("error:2")

	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if !strings.Contains(state.LastError, "macro M91 failed at line 1") {
		t.Fatalf("LastError = %q, want macro context", state.LastError)
	}
	if !strings.Contains(state.LastError, "query command failed: error:2") {
		t.Fatalf("LastError = %q, want query failure text", state.LastError)
	}
	ensureWritesStayAt(t, fake, 1, 100*time.Millisecond)
}

func TestControllerReadWCSOffsetsUsesQueryHelper(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "macro-read-wcs.gcode", "M92\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	reg := macro.NewRegistry()
	received := make(chan macro.WCSOffsets, 1)
	reg.Register(92, macro.HandlerFunc(func(ctx context.Context, runtime macro.Runtime, inv macro.Invocation) error {
		offsets, err := runtime.ReadWCSOffsets(ctx)
		if err != nil {
			return err
		}
		received <- offsets
		return nil
	}))
	controller.SetMacroEngine(macro.NewEngine(reg))
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program macro-read-wcs.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("query write = %q, want %q", got, want)
	}

	fake.InjectRX("[G54:1.000,2.000,3.000]")
	fake.InjectRX("[G55:4.000,5.000,6.000]")
	fake.InjectRX("ok")

	var offsets macro.WCSOffsets
	select {
	case offsets = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for parsed offsets")
	}
	if got, want := offsets[macro.WCS("G54")], (macro.Point{X: 1, Y: 2, Z: 3}); got != want {
		t.Fatalf("G54 offsets = %+v, want %+v", got, want)
	}
	if got, want := offsets[macro.WCS("G55")], (macro.Point{X: 4, Y: 5, Z: 6}); got != want {
		t.Fatalf("G55 offsets = %+v, want %+v", got, want)
	}
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerNormalProgramSendIgnoresIntermediateRX(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "normal-intermediate.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program normal-intermediate.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "G0 X1"; got != want {
		t.Fatalf("written line = %q, want %q", got, want)
	}

	fake.InjectRX("<Idle|MPos:0,0,0>")
	fake.InjectRX("[GC:G0 G54 G17]")
	ensureWritesStayAt(t, fake, 1, 100*time.Millisecond)
	if got := controller.Snapshot().ProgramStatus; got != ProgramRunning {
		t.Fatalf("ProgramStatus after intermediate RX = %q, want %q", got, ProgramRunning)
	}

	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerNormalLongRunningLineIgnoresNonTerminalRXBurst(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "normal-rx-burst.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program normal-rx-burst.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "G0 X1"; got != want {
		t.Fatalf("written line = %q, want %q", got, want)
	}

	for i := 0; i < 300; i++ {
		fake.InjectRX(fmt.Sprintf("<Run|MPos:%d,0,0>", i))
	}
	ensureWritesStayAt(t, fake, 1, 100*time.Millisecond)
	if got := controller.Snapshot().ProgramStatus; got != ProgramRunning {
		t.Fatalf("ProgramStatus after RX burst = %q, want %q", got, ProgramRunning)
	}

	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerSendLineCollectingResponsesRejectsConcurrentQuery(t *testing.T) {
	t.Parallel()

	controller := NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))
	run := &programRun{
		rxCh:      make(chan string, 1),
		queryRxCh: make(chan string, 1),
	}

	controller.mu.Lock()
	controller.run = run
	controller.state.ProgramStatus = ProgramRunning
	controller.mu.Unlock()

	_, err := controller.sendLineCollectingResponses(context.Background(), run, "$#")
	if !errors.Is(err, ErrProgramQueryActive) {
		t.Fatalf("err = %v, want %v", err, ErrProgramQueryActive)
	}
}

func TestControllerSendLineCollectingResponsesClearsQueryChannelOnFailure(t *testing.T) {
	t.Parallel()

	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	if err := controller.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_ = waitForEvent(t, controller.Events(), EventStateChanged)
	run := &programRun{rxCh: make(chan string, 1)}
	controller.mu.Lock()
	controller.run = run
	controller.state.ProgramStatus = ProgramRunning
	controller.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		_, err := controller.sendLineCollectingResponses(context.Background(), run, "$#")
		errCh <- err
	}()
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("query write = %q, want %q", got, want)
	}
	fake.InjectRX("error:2")

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "query command failed: error:2") {
			t.Fatalf("err = %v, want query failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for query failure")
	}

	controller.mu.Lock()
	defer controller.mu.Unlock()
	if run.queryRxCh != nil {
		t.Fatalf("run.queryRxCh = %v, want nil", run.queryRxCh)
	}
}

func TestControllerMotionRewrite(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "rewrite.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	controller.SetMotionRewriter(testRewriter{line: "G0 X2"})
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program rewrite.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "G0 X2"; got != want {
		t.Fatalf("written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramCompleted })
}

func TestControllerMotionRewriteErrorFailsProgram(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "rewrite-fail.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
	controller.SetMotionRewriter(testRewriter{err: errors.New("surface missing")})
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program rewrite-fail.gcode")
	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if got, want := state.LastError, "rewrite line 1: surface missing"; got != want {
		t.Fatalf("LastError = %q, want %q", got, want)
	}
	if got := len(fake.Written()); got != 0 {
		t.Fatalf("len(written) = %d, want 0", got)
	}
}

func TestControllerDefaultM108FailurePreventsLaterGCode(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "default-macro-missing-var.gcode", "M108 missing G54Z\nG0 X9\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program default-macro-missing-var.gcode")
	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if !strings.Contains(state.LastError, "macro M108 failed at line 1") || !strings.Contains(state.LastError, "unknown variable") {
		t.Fatalf("LastError = %q, want macro context and unknown variable", state.LastError)
	}
	if got := len(fake.Written()); got != 0 {
		t.Fatalf("len(written) = %d, want 0; writes=%#v", got, fake.Written())
	}
}

func TestControllerDefaultMacrosDoNotInterceptDottedMCode(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "dotted-mcode.gcode", "M107.1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program dotted-mcode.gcode")
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "M107.1"; got != want {
		t.Fatalf("written line = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
}

func TestControllerFinishProgramFailureCancelsActiveRun(t *testing.T) {
	t.Parallel()

	controller := NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &programRun{rxCh: make(chan string, 1), cancel: cancel}
	controller.mu.Lock()
	controller.run = run
	controller.state.ProgramStatus = ProgramRunning
	controller.mu.Unlock()

	controller.finishProgramFailure(run, errors.New("forced failure"))

	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("run context error = %v, want %v", err, context.Canceled)
	}
	state := waitForState(t, controller, func(s State) bool { return s.ProgramStatus == ProgramFailed })
	if got, want := state.LastError, "forced failure"; got != want {
		t.Fatalf("LastError = %q, want %q", got, want)
	}
	errEv := waitForEvent(t, controller.Events(), EventError)
	if got, want := errEv.Text, "forced failure"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestControllerFinishProgramFailureIgnoresNilRun(t *testing.T) {
	t.Parallel()

	controller := NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))
	before := controller.Snapshot()

	controller.finishProgramFailure(nil, errors.New("nil run failure"))

	after := controller.Snapshot()
	if got, want := after.ProgramStatus, before.ProgramStatus; got != want {
		t.Fatalf("ProgramStatus = %q, want %q", got, want)
	}
	if after.LastError == "nil run failure" {
		t.Fatalf("LastError = %q, want unchanged", after.LastError)
	}

	ensureNoEvent(t, controller.Events(), 100*time.Millisecond)
}

func TestControllerFinishProgramFailureIgnoresStaleRun(t *testing.T) {
	t.Parallel()

	controller := NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	run1 := &programRun{rxCh: make(chan string, 1), cancel: cancel1}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	run2 := &programRun{rxCh: make(chan string, 1), cancel: cancel2}

	controller.mu.Lock()
	controller.run = run2
	controller.state.ProgramStatus = ProgramRunning
	controller.mu.Unlock()

	controller.finishProgramFailure(run1, context.Canceled)

	if err := ctx1.Err(); err != nil {
		t.Fatalf("stale run context was canceled: %v", err)
	}
	if err := ctx2.Err(); err != nil {
		t.Fatalf("active run context was canceled: %v", err)
	}
	if got := controller.Snapshot().ProgramStatus; got != ProgramRunning {
		t.Fatalf("ProgramStatus = %q, want %q", got, ProgramRunning)
	}

	ensureNoEvent(t, controller.Events(), 100*time.Millisecond)
}

func TestControllerForcedFailureDoesNotEmitContextCanceledError(t *testing.T) {
	t.Parallel()

	path := writeProgramFile(t, "forced-failure.gcode", "G0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second

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
	_ = waitForEventText(t, controller.Events(), EventStateChanged, "started program forced-failure.gcode")
	waitForWrites(t, fake, 1)

	controller.mu.RLock()
	run := controller.run
	controller.mu.RUnlock()
	if run == nil {
		t.Fatal("controller.run = nil, want active run")
	}

	controller.finishProgramFailure(run, errors.New("forced failure"))

	errEv := waitForEvent(t, controller.Events(), EventError)
	if got, want := errEv.Text, "forced failure"; got != want {
		t.Fatalf("first error event = %q, want %q", got, want)
	}

	ensureNoErrorEvent(t, controller.Events(), 150*time.Millisecond)
}

func TestControllerDefaultM102NumericExpressionWritesWithoutWCSQuery(t *testing.T) {
	t.Parallel()
	path := writeProgramFile(t, "default-m102-numeric.gcode", "M102 G54Z = (1 + 2) * 3\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "G10 L2 P1 Z9.000000"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
	for _, w := range fake.Written() {
		if strings.HasPrefix(w.Display, "M102") || w.Display == "$#" {
			t.Fatalf("unexpected write: %#v", fake.Written())
		}
	}
}

func TestControllerDefaultM102WCSExpressionReadsOnceAndWrites(t *testing.T) {
	t.Parallel()
	path := writeProgramFile(t, "default-m102-wcs.gcode", "M102 G56Z = (G54Z + G55Z) / 2\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	waitForWrites(t, fake, 1)
	if fake.Written()[0].Display != "$#" {
		t.Fatalf("first = %q", fake.Written()[0].Display)
	}
	fake.InjectRX("[G54:0.000,0.000,1.000]")
	fake.InjectRX("[G55:0.000,0.000,5.000]")
	fake.InjectRX("[G56:0.000,0.000,0.000]")
	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "G10 L2 P3 Z3.000000"; got != want {
		t.Fatalf("second = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 1 && s.ProgramStatus == ProgramCompleted })
	for _, written := range fake.Written() {
		if strings.HasPrefix(written.Display, "M102") {
			t.Fatalf("raw M102 was sent: %#v", fake.Written())
		}
	}
	if got := countDisplayWrites(fake.Written(), "$#"); got != 1 {
		t.Fatalf("WCS query count = %d, want 1; writes=%#v", got, fake.Written())
	}
}

func TestControllerDefaultM102FailurePreventsNextLine(t *testing.T) {
	t.Parallel()
	path := writeProgramFile(t, "default-m102-fail.gcode", "M102 G54Z = 1 / 0\nG0 X9\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	waitForState(t, controller, func(s State) bool {
		return s.ProgramStatus == ProgramFailed && strings.Contains(s.LastError, "macro M102 failed at line 1") && strings.Contains(s.LastError, "division by zero")
	})
	for _, w := range fake.Written() {
		if w.Display == "G0 X9" || strings.HasPrefix(w.Display, "M102") {
			t.Fatalf("unexpected write: %#v", fake.Written())
		}
	}
}

func TestControllerDefaultM106PassAllowsNextLine(t *testing.T) {
	t.Parallel()
	path := writeProgramFile(t, "default-m106-pass.gcode", "M106 G54Z <= G55Z\nG0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	waitForWrites(t, fake, 1)
	if fake.Written()[0].Display != "$#" {
		t.Fatalf("first = %q", fake.Written()[0].Display)
	}
	fake.InjectRX("[G54:0.000,0.000,1.000]")
	fake.InjectRX("[G55:0.000,0.000,2.000]")
	fake.InjectRX("ok")
	waitForWrites(t, fake, 2)
	if got, want := fake.Written()[1].Display, "G0 X1"; got != want {
		t.Fatalf("second = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 2 && s.ProgramStatus == ProgramCompleted })
	for _, written := range fake.Written() {
		if strings.HasPrefix(written.Display, "M106") {
			t.Fatalf("raw M106 was sent: %#v", fake.Written())
		}
	}
}

func TestControllerDefaultM106FailurePreventsNextLine(t *testing.T) {
	t.Parallel()
	path := writeProgramFile(t, "default-m106-fail.gcode", "M106 G54Z <= G55Z ERROR expected G54Z to be below G55Z\nG0 X9\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "$#"; got != want {
		t.Fatalf("first = %q, want %q", got, want)
	}
	fake.InjectRX("[G54:0.000,0.000,3.000]")
	fake.InjectRX("[G55:0.000,0.000,2.000]")
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool {
		return s.ProgramStatus == ProgramFailed && strings.Contains(s.LastError, "macro M106 failed at line 1") && strings.Contains(s.LastError, "expected G54Z")
	})
	for _, w := range fake.Written() {
		if w.Display == "G0 X9" || strings.HasPrefix(w.Display, "M106") {
			t.Fatalf("unexpected write: %#v", fake.Written())
		}
	}
}

func TestControllerDefaultM106NumericAssertionDoesNotReadWCS(t *testing.T) {
	t.Parallel()
	path := writeProgramFile(t, "default-m106-numeric.gcode", "M106 1 < 2\nG0 X1\n")
	fake := transport.NewFakeTransport()
	controller := NewController(fake, ports.StaticList(nil, nil))
	controller.statusPollInterval = 10 * time.Second
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
	waitForWrites(t, fake, 1)
	if got, want := fake.Written()[0].Display, "G0 X1"; got != want {
		t.Fatalf("first = %q, want %q", got, want)
	}
	fake.InjectRX("ok")
	waitForState(t, controller, func(s State) bool { return s.ProgramComplete == 2 && s.ProgramStatus == ProgramCompleted })
	for _, written := range fake.Written() {
		if strings.HasPrefix(written.Display, "M106") {
			t.Fatalf("raw M106 was sent: %#v", fake.Written())
		}
		if written.Display == "$#" {
			t.Fatalf("unexpected WCS query: %#v", fake.Written())
		}
	}
}
