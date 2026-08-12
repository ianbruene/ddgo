package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

func TestControllerSpindleOperations(t *testing.T) {
	fake := transport.NewFakeTransport()
	c := NewController(fake, ports.StaticList(nil, nil))
	c.statusPollInterval = 10 * time.Second
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("mock")); err != nil {
		t.Fatal(err)
	}
	_ = waitForEvent(t, c.Events(), EventStateChanged)
	operations := []struct {
		want string
		call func() error
	}{
		{"S5000 M3", func() error { return c.StartSpindleCW(context.Background(), 5000) }},
		{"S6000 M4", func() error { return c.StartSpindleCCW(context.Background(), 6000) }},
		{"S7000", func() error { return c.SetSpindleSpeed(context.Background(), 7000) }},
		{"M5", func() error { return c.StopSpindle(context.Background()) }},
	}
	for i, op := range operations {
		if err := op.call(); err != nil {
			t.Fatalf("operation %d: %v", i, err)
		}
		_ = waitForEvent(t, c.Events(), EventConsoleTX)
		written := fake.Written()
		if got := written[len(written)-1].Display; got != op.want {
			t.Fatalf("display = %q, want %q", got, op.want)
		}
		fake.InjectRX("ok")
		_ = waitForEvent(t, c.Events(), EventConsoleRX)
	}
}

func TestControllerSpindleOperationsObeyManualAdmission(t *testing.T) {
	fake := transport.NewFakeTransport()
	c := NewController(fake, ports.StaticList(nil, nil))
	c.statusPollInterval = 10 * time.Second
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("mock")); err != nil {
		t.Fatal(err)
	}
	_ = waitForEvent(t, c.Events(), EventStateChanged)
	c.mu.Lock()
	c.state.ProgramStatus = ProgramRunning
	c.mu.Unlock()
	if err := c.StartSpindleCW(context.Background(), 5000); !errors.Is(err, ErrProgramActive) {
		t.Fatalf("error = %v", err)
	}
	_ = waitForEvent(t, c.Events(), EventError)
	if len(fake.Written()) != 0 {
		t.Fatal("rejected spindle operation wrote to transport")
	}
}
