package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/cncui/internal/grbl"
	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
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

	if err := controller.Connect(context.Background(), transport.PortConfig{BaudRate: transport.DefaultBaudRate}); err == nil {
		t.Fatal("Connect() with empty port name error = nil, want non-nil")
	}
	ev := waitForEvent(t, controller.Events(), EventError)
	if got, want := ev.Text, "port name is required"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}

	if err := controller.Connect(context.Background(), transport.PortConfig{Name: "/dev/ttyACM0", BaudRate: 0}); err == nil {
		t.Fatal("Connect() with invalid baud error = nil, want non-nil")
	}
	ev = waitForEvent(t, controller.Events(), EventError)
	if got, want := ev.Text, "baud rate must be greater than zero"; got != want {
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
