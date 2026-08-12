package transport

import (
	"context"
	"sync"
	"time"
)

type FakeTransport struct {
	mu             sync.Mutex
	open           bool
	cfg            PortConfig
	writes         []Message
	events         chan Event
	openErr        error
	writeErr       error
	closeErr       error
	generation     ConnectionGeneration
	nextGeneration ConnectionGeneration
}

func NewFakeTransport() *FakeTransport {
	return &FakeTransport{events: make(chan Event, 256)}
}

func (f *FakeTransport) SetOpenError(err error)  { f.mu.Lock(); f.openErr = err; f.mu.Unlock() }
func (f *FakeTransport) SetWriteError(err error) { f.mu.Lock(); f.writeErr = err; f.mu.Unlock() }
func (f *FakeTransport) SetCloseError(err error) { f.mu.Lock(); f.closeErr = err; f.mu.Unlock() }

func (f *FakeTransport) Open(_ context.Context, cfg PortConfig) (ConnectionGeneration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return 0, f.openErr
	}
	f.nextGeneration++
	if f.nextGeneration == 0 {
		f.nextGeneration++
	}
	f.generation = f.nextGeneration
	f.open = true
	f.cfg = cfg
	f.events <- Event{Kind: EventConnected, Generation: f.generation, When: time.Now(), Text: cfg.Name}
	return f.generation, nil
}

func (f *FakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return f.closeErr
	}
	if f.open {
		generation := f.generation
		f.open = false
		f.generation = 0
		f.events <- Event{Kind: EventDisconnected, Generation: generation, When: time.Now()}
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
	copied := msg
	generation := f.generation
	copied.Payload = append([]byte(nil), msg.Payload...)
	f.writes = append(f.writes, copied)
	f.events <- Event{Kind: EventTX, Generation: generation, When: time.Now(), Text: msg.Display, Payload: append([]byte(nil), msg.Payload...), SuppressLog: msg.SuppressLog}
	return nil
}

func (f *FakeTransport) Events() <-chan Event { return f.events }

func (f *FakeTransport) InjectRX(line string) {
	f.mu.Lock()
	generation := f.generation
	f.mu.Unlock()
	f.InjectRXForGeneration(generation, line)
}

func (f *FakeTransport) InjectRXForGeneration(generation ConnectionGeneration, line string) {
	f.events <- Event{Kind: EventRX, Generation: generation, When: time.Now(), Text: line, Payload: []byte(line)}
}

func (f *FakeTransport) InjectError(err error) {
	f.mu.Lock()
	generation := f.generation
	f.mu.Unlock()
	f.InjectErrorForGeneration(generation, err)
}

func (f *FakeTransport) InjectErrorForGeneration(generation ConnectionGeneration, err error) {
	f.events <- Event{Kind: EventError, Generation: generation, When: time.Now(), Err: err, Text: err.Error()}
}

func (f *FakeTransport) InjectDisconnected() {
	f.mu.Lock()
	generation := f.generation
	f.open = false
	f.generation = 0
	f.mu.Unlock()
	f.InjectDisconnectedForGeneration(generation)
}

func (f *FakeTransport) InjectDisconnectedForGeneration(generation ConnectionGeneration) {
	f.events <- Event{Kind: EventDisconnected, Generation: generation, When: time.Now()}
}

func (f *FakeTransport) Generation() ConnectionGeneration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation
}

func (f *FakeTransport) Written() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Message, len(f.writes))
	copy(out, f.writes)
	for i := range out {
		out[i].Payload = append([]byte(nil), out[i].Payload...)
	}
	return out
}
