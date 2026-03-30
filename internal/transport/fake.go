package transport

import (
	"context"
	"sync"
	"time"
)

type FakeTransport struct {
	mu       sync.Mutex
	open     bool
	cfg      PortConfig
	writes   []Message
	events   chan Event
	openErr  error
	writeErr error
	closeErr error
}

func NewFakeTransport() *FakeTransport {
	return &FakeTransport{events: make(chan Event, 256)}
}

func (f *FakeTransport) SetOpenError(err error)  { f.mu.Lock(); f.openErr = err; f.mu.Unlock() }
func (f *FakeTransport) SetWriteError(err error) { f.mu.Lock(); f.writeErr = err; f.mu.Unlock() }
func (f *FakeTransport) SetCloseError(err error) { f.mu.Lock(); f.closeErr = err; f.mu.Unlock() }

func (f *FakeTransport) Open(_ context.Context, cfg PortConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return f.openErr
	}
	f.open = true
	f.cfg = cfg
	f.events <- Event{Kind: EventConnected, When: time.Now(), Text: cfg.Name}
	return nil
}

func (f *FakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return f.closeErr
	}
	if f.open {
		f.open = false
		f.events <- Event{Kind: EventDisconnected, When: time.Now()}
	}
	return nil
}

func (f *FakeTransport) Write(_ context.Context, msg Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open {
		return ErrNotOpen
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, msg)
	f.events <- Event{Kind: EventTX, When: time.Now(), Text: msg.Display, Payload: append([]byte(nil), msg.Payload...)}
	return nil
}

func (f *FakeTransport) Events() <-chan Event { return f.events }

func (f *FakeTransport) InjectRX(line string) {
	f.events <- Event{Kind: EventRX, When: time.Now(), Text: line, Payload: []byte(line)}
}

func (f *FakeTransport) InjectError(err error) {
	f.events <- Event{Kind: EventError, When: time.Now(), Err: err, Text: err.Error()}
}

func (f *FakeTransport) Written() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Message, len(f.writes))
	copy(out, f.writes)
	return out
}
