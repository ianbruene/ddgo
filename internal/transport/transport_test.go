package transport

import (
	"testing"
)

func TestDefaultPortConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultPortConfig("/dev/ttyACM0")
	if got, want := cfg.Name, "/dev/ttyACM0"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := cfg.BaudRate, DefaultBaudRate; got != want {
		t.Fatalf("BaudRate = %d, want %d", got, want)
	}
	if got, want := cfg.DataBits, 8; got != want {
		t.Fatalf("DataBits = %d, want %d", got, want)
	}
	if got, want := cfg.StopBits, 1; got != want {
		t.Fatalf("StopBits = %d, want %d", got, want)
	}
	if got, want := cfg.Parity, "N"; got != want {
		t.Fatalf("Parity = %q, want %q", got, want)
	}
}

func TestNewLineMessage_NormalizesLineEnding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantPayload string
		wantDisplay string
	}{
		{name: "plain", input: "G0 X10", wantPayload: "G0 X10\n", wantDisplay: "G0 X10"},
		{name: "trailing newline", input: "G0 X10\n", wantPayload: "G0 X10\n", wantDisplay: "G0 X10"},
		{name: "trailing carriage return newline", input: "G0 X10\r\n", wantPayload: "G0 X10\n", wantDisplay: "G0 X10"},
		{name: "multiple line endings", input: "G0 X10\n\n", wantPayload: "G0 X10\n", wantDisplay: "G0 X10"},
		{name: "empty", input: "", wantPayload: "\n", wantDisplay: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			msg := NewLineMessage(tt.input)
			if got := string(msg.Payload); got != tt.wantPayload {
				t.Fatalf("Payload = %q, want %q", got, tt.wantPayload)
			}
			if got := msg.Display; got != tt.wantDisplay {
				t.Fatalf("Display = %q, want %q", got, tt.wantDisplay)
			}
		})
	}
}

func TestNewRawMessage_CopiesPayload(t *testing.T) {
	t.Parallel()

	original := []byte("abc")
	msg := NewRawMessage(original, "abc")
	original[0] = 'z'

	if got, want := string(msg.Payload), "abc"; got != want {
		t.Fatalf("Payload = %q, want %q", got, want)
	}
	if got, want := msg.Display, "abc"; got != want {
		t.Fatalf("Display = %q, want %q", got, want)
	}
}
