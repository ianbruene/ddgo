//go:build serial

package transport

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

const noUnexpectedTransportEventWindow = 100 * time.Millisecond

type scriptedRead struct {
	data []byte
	err  error
}

type scriptedSerialPort struct {
	readCh  chan scriptedRead
	writeCh chan []byte

	mu     sync.Mutex
	closed bool
	once   sync.Once
}

func newScriptedSerialPort() *scriptedSerialPort {
	return &scriptedSerialPort{
		readCh:  make(chan scriptedRead, 8),
		writeCh: make(chan []byte, 8),
	}
}

func (p *scriptedSerialPort) Read(buf []byte) (int, error) {
	result, ok := <-p.readCh
	if !ok {
		return 0, io.EOF
	}
	n := copy(buf, result.data)
	return n, result.err
}

func (p *scriptedSerialPort) Write(payload []byte) (int, error) {
	cp := append([]byte(nil), payload...)
	p.writeCh <- cp
	return len(payload), nil
}

func (p *scriptedSerialPort) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		close(p.readCh)
	})
	return nil
}

func newTestSerialTransport(port serialPort) *SerialTransport {
	tr := &SerialTransport{
		port:   port,
		events: make(chan Event, 256),
		closed: make(chan struct{}),
	}
	tr.wg.Add(1)
	go tr.readLoop(tr.closed, port)
	return tr
}

func waitForNextSerialTransportEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transport event")
		return Event{}
	}
}

func assertNoTransportEvent(t *testing.T, ch <-chan Event, duration time.Duration) {
	t.Helper()
	select {
	case event := <-ch:
		t.Fatalf("unexpected transport event: kind=%q err=%v", event.Kind, event.Err)
	case <-time.After(duration):
	}
}

func assertSerialTransportClosed(t *testing.T, tr *SerialTransport) {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.port != nil {
		t.Errorf("port = %v, want nil", tr.port)
	}
	if tr.closed != nil {
		t.Errorf("closed = %v, want nil", tr.closed)
	}
}

func TestSerialTransportConsume_SplitsLinesAndSkipsEmptyTerminators(t *testing.T) {
	t.Parallel()

	tr := &SerialTransport{events: make(chan Event, 16)}
	tr.consume([]byte("ok\r\n<Idle|MPos:0,0,0>\n\nerror:1\r"))

	for i, want := range []string{"ok", "<Idle|MPos:0,0,0>", "error:1"} {
		event := waitForNextSerialTransportEvent(t, tr.events)
		if event.Kind != EventRX {
			t.Fatalf("event %d kind = %q, want %q", i, event.Kind, EventRX)
		}
		if event.Text != want {
			t.Fatalf("event %d text = %q, want %q", i, event.Text, want)
		}
	}
}

func TestSerialTransportConsume_PartialChunks(t *testing.T) {
	t.Parallel()

	tr := &SerialTransport{events: make(chan Event, 16)}
	tr.consume([]byte("<Id"))
	select {
	case event := <-tr.events:
		t.Fatalf("unexpected event before line complete: %+v", event)
	default:
	}

	tr.consume([]byte("le|MPos:0,0,0>\n"))
	event := waitForNextSerialTransportEvent(t, tr.events)
	if event.Kind != EventRX {
		t.Fatalf("event kind = %q, want %q", event.Kind, EventRX)
	}
	if got, want := event.Text, "<Idle|MPos:0,0,0>"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
}

func TestSerialTransportReadLoopEOFEmitsDisconnectedAndClearsPort(t *testing.T) {
	port := newScriptedSerialPort()
	tr := newTestSerialTransport(port)

	port.readCh <- scriptedRead{err: io.EOF}

	event := waitForNextSerialTransportEvent(t, tr.events)
	if event.Kind != EventDisconnected {
		t.Fatalf("event kind = %q, want %q", event.Kind, EventDisconnected)
	}
	assertSerialTransportClosed(t, tr)

	if err := tr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertNoTransportEvent(t, tr.events, noUnexpectedTransportEventWindow)
}

func TestSerialTransportReadLoopErrorEmitsErrorThenDisconnectedAndClearsPort(t *testing.T) {
	port := newScriptedSerialPort()
	tr := newTestSerialTransport(port)
	boom := errors.New("read failed")

	port.readCh <- scriptedRead{err: boom}

	errorEvent := waitForNextSerialTransportEvent(t, tr.events)
	if errorEvent.Kind != EventError {
		t.Fatalf("first event kind = %q, want %q", errorEvent.Kind, EventError)
	}
	if errorEvent.Err != boom {
		t.Errorf("error event Err = %v, want %v", errorEvent.Err, boom)
	}
	if !strings.Contains(errorEvent.Text, "read failed") {
		t.Errorf("error event Text = %q, want it to contain %q", errorEvent.Text, "read failed")
	}
	disconnectedEvent := waitForNextSerialTransportEvent(t, tr.events)
	if disconnectedEvent.Kind != EventDisconnected {
		t.Fatalf("second event kind = %q, want %q", disconnectedEvent.Kind, EventDisconnected)
	}
	assertSerialTransportClosed(t, tr)
}

func TestSerialTransportExplicitCloseDoesNotEmitUnexpectedError(t *testing.T) {
	port := newScriptedSerialPort()
	tr := newTestSerialTransport(port)

	if err := tr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	event := waitForNextSerialTransportEvent(t, tr.events)
	if event.Kind != EventDisconnected {
		t.Fatalf("event kind = %q, want %q", event.Kind, EventDisconnected)
	}
	assertNoTransportEvent(t, tr.events, noUnexpectedTransportEventWindow)
	assertSerialTransportClosed(t, tr)
}

func TestSerialTransportConsumesCompleteLines(t *testing.T) {
	port := newScriptedSerialPort()
	tr := newTestSerialTransport(port)
	port.readCh <- scriptedRead{data: []byte("ok\r\n<Idle|M:0.000,0.000,0.000>\n")}

	for _, want := range []string{"ok", "<Idle|M:0.000,0.000,0.000>"} {
		event := waitForNextSerialTransportEvent(t, tr.events)
		if event.Kind != EventRX {
			t.Fatalf("event kind = %q, want %q", event.Kind, EventRX)
		}
		if event.Text != want {
			t.Errorf("event Text = %q, want %q", event.Text, want)
		}
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestSerialTransportReadLoopDiscardsPartialLineFromPriorConnection(t *testing.T) {
	port := newScriptedSerialPort()
	tr := &SerialTransport{events: make(chan Event, 16)}
	tr.consume([]byte("stale partial response"))

	tr.port = port
	tr.closed = make(chan struct{})
	tr.wg.Add(1)
	go tr.readLoop(tr.closed, port)
	port.readCh <- scriptedRead{data: []byte("ok\n")}

	event := waitForNextSerialTransportEvent(t, tr.events)
	if event.Kind != EventRX {
		t.Fatalf("event kind = %q, want %q", event.Kind, EventRX)
	}
	if event.Text != "ok" {
		t.Fatalf("event Text = %q, want %q", event.Text, "ok")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
