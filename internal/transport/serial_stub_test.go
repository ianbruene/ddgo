//go:build !serial

package transport

import (
	"context"
	"errors"
	"testing"
)

func TestSerialTransportStub(t *testing.T) {
	t.Parallel()

	tpt := NewSerialTransport()
	if err := tpt.Open(context.Background(), DefaultPortConfig("/dev/ttyACM0")); !errors.Is(err, ErrSerialTransportNotBuilt) {
		t.Fatalf("Open() error = %v, want %v", err, ErrSerialTransportNotBuilt)
	}
	if err := tpt.Write(context.Background(), NewLineMessage("?")); !errors.Is(err, ErrSerialTransportNotBuilt) {
		t.Fatalf("Write() error = %v, want %v", err, ErrSerialTransportNotBuilt)
	}
	if err := tpt.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if tpt.Events() == nil {
		t.Fatal("Events() = nil, want non-nil channel")
	}
}
