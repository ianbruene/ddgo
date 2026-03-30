//go:build serial

package transport

import (
	"testing"
	"time"
)

func TestSerialTransportConsume_SplitsLinesAndSkipsEmptyTerminators(t *testing.T) {
	t.Parallel()

	tpt := &SerialTransport{events: make(chan Event, 16)}
	tpt.consume([]byte("ok\r\n<Idle|MPos:0,0,0>\n\nerror:1\r"))

	want := []string{"ok", "<Idle|MPos:0,0,0>", "error:1"}
	for i, text := range want {
		ev := waitForSerialRealEvent(t, tpt.events, EventRX)
		if got := ev.Text; got != text {
			t.Fatalf("event %d text = %q, want %q", i, got, text)
		}
	}
}

func TestSerialTransportConsume_PartialChunks(t *testing.T) {
	t.Parallel()

	tpt := &SerialTransport{events: make(chan Event, 16)}
	tpt.consume([]byte("<Id"))
	select {
	case ev := <-tpt.events:
		t.Fatalf("unexpected event before line complete: %+v", ev)
	default:
	}

	tpt.consume([]byte("le|MPos:0,0,0>\n"))
	ev := waitForSerialRealEvent(t, tpt.events, EventRX)
	if got, want := ev.Text, "<Idle|MPos:0,0,0>"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
}

func TestFirstNonZero(t *testing.T) {
	t.Parallel()

	if got, want := firstNonZero(8, 7), 8; got != want {
		t.Fatalf("firstNonZero(non-zero) = %d, want %d", got, want)
	}
	if got, want := firstNonZero(0, 7), 7; got != want {
		t.Fatalf("firstNonZero(zero) = %d, want %d", got, want)
	}
}

func waitForSerialRealEvent(t *testing.T, ch <-chan Event, kind EventKind) Event {
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
