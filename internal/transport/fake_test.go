package transport

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeTransport_OpenWriteCloseLifecycle(t *testing.T) {
	t.Parallel()

	f := NewFakeTransport()
	cfg := DefaultPortConfig("/dev/ttyACM0")
	if err := f.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	connected := waitForTransportEvent(t, f.Events(), EventConnected)
	if got, want := connected.Text, cfg.Name; got != want {
		t.Fatalf("connected event text = %q, want %q", got, want)
	}

	msg := NewLineMessage("?")
	if err := f.Write(context.Background(), msg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	tx := waitForTransportEvent(t, f.Events(), EventTX)
	if got, want := tx.Text, msg.Display; got != want {
		t.Fatalf("tx text = %q, want %q", got, want)
	}
	if got, want := string(tx.Payload), string(msg.Payload); got != want {
		t.Fatalf("tx payload = %q, want %q", got, want)
	}

	written := f.Written()
	if got, want := len(written), 1; got != want {
		t.Fatalf("len(Written()) = %d, want %d", got, want)
	}
	if got, want := string(written[0].Payload), string(msg.Payload); got != want {
		t.Fatalf("written payload = %q, want %q", got, want)
	}

	f.InjectRX("ok")
	rx := waitForTransportEvent(t, f.Events(), EventRX)
	if got, want := rx.Text, "ok"; got != want {
		t.Fatalf("rx text = %q, want %q", got, want)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	_ = waitForTransportEvent(t, f.Events(), EventDisconnected)
}

func TestFakeTransport_WriteWhenClosed(t *testing.T) {
	t.Parallel()

	f := NewFakeTransport()
	if err := f.Write(context.Background(), NewLineMessage("G0 X1")); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("Write() error = %v, want %v", err, ErrNotOpen)
	}
}

func TestFakeTransport_ConfiguredErrors(t *testing.T) {
	t.Parallel()

	f := NewFakeTransport()
	openErr := errors.New("open failed")
	f.SetOpenError(openErr)
	if err := f.Open(context.Background(), DefaultPortConfig("/dev/ttyACM0")); !errors.Is(err, openErr) {
		t.Fatalf("Open() error = %v, want %v", err, openErr)
	}

	f = NewFakeTransport()
	if err := f.Open(context.Background(), DefaultPortConfig("/dev/ttyACM0")); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	writeErr := errors.New("write failed")
	f.SetWriteError(writeErr)
	if err := f.Write(context.Background(), NewLineMessage("G0 X1")); !errors.Is(err, writeErr) {
		t.Fatalf("Write() error = %v, want %v", err, writeErr)
	}

	closeErr := errors.New("close failed")
	f.SetCloseError(closeErr)
	if err := f.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want %v", err, closeErr)
	}
}

func TestFakeTransport_InjectError(t *testing.T) {
	t.Parallel()

	f := NewFakeTransport()
	wantErr := errors.New("injected fault")
	f.InjectError(wantErr)

	ev := waitForTransportEvent(t, f.Events(), EventError)
	if !errors.Is(ev.Err, wantErr) {
		t.Fatalf("error event Err = %v, want %v", ev.Err, wantErr)
	}
	if got, want := ev.Text, wantErr.Error(); got != want {
		t.Fatalf("error event Text = %q, want %q", got, want)
	}
}

func waitForTransportEvent(t *testing.T, ch <-chan Event, kind EventKind) Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for transport event kind %q", kind)
		}
	}
}
