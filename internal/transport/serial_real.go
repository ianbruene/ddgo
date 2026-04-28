//go:build serial

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

const serialArduinoBaudRate = 115200

type SerialTransport struct {
	mu      sync.Mutex
	port    serial.Port
	cfg     PortConfig
	events  chan Event
	closed  chan struct{}
	wg      sync.WaitGroup
	readBuf bytes.Buffer
}

func NewSerialTransport() *SerialTransport {
	return &SerialTransport{events: make(chan Event, 256)}
}

func (t *SerialTransport) Events() <-chan Event { return t.events }

func (t *SerialTransport) Open(_ context.Context, cfg PortConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.port != nil {
		return errors.New("serial port already open")
	}
	mode := &serial.Mode{
		BaudRate: serialArduinoBaudRate,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}
	port, err := serial.Open(cfg.Name, mode)
	if err != nil {
		return err
	}
	t.port = port
	t.cfg = cfg
	t.closed = make(chan struct{})
	t.wg.Add(1)
	go t.readLoop(t.closed, port)
	t.events <- Event{Kind: EventConnected, When: time.Now(), Text: cfg.Name}
	return nil
}

func (t *SerialTransport) Close() error {
	t.mu.Lock()
	port := t.port
	closed := t.closed
	if port == nil {
		t.mu.Unlock()
		return nil
	}
	t.port = nil
	t.closed = nil
	t.mu.Unlock()

	if closed != nil {
		close(closed)
	}
	err := port.Close()
	t.wg.Wait()
	t.events <- Event{Kind: EventDisconnected, When: time.Now()}
	return err
}

func (t *SerialTransport) Write(ctx context.Context, msg Message) error {
	t.mu.Lock()
	port := t.port
	t.mu.Unlock()
	if port == nil {
		return ErrNotOpen
	}
	for len(msg.Payload) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := port.Write(msg.Payload)
		if err != nil {
			t.events <- Event{Kind: EventError, When: time.Now(), Err: err}
			return err
		}
		msg.Payload = msg.Payload[n:]
	}
	t.events <- Event{Kind: EventTX, When: time.Now(), Text: msg.Display}
	return nil
}

func (t *SerialTransport) readLoop(closed <-chan struct{}, port serial.Port) {
	defer t.wg.Done()
	buf := make([]byte, 256)
	for {
		select {
		case <-closed:
			return
		default:
		}
		n, err := port.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(strings.ToLower(err.Error()), "closed") {
				return
			}
			t.events <- Event{Kind: EventError, When: time.Now(), Err: err}
			return
		}
		if n == 0 {
			continue
		}
		t.consume(buf[:n])
	}
}

func (t *SerialTransport) consume(chunk []byte) {
	for _, b := range chunk {
		switch b {
		case '\n', '\r':
			if t.readBuf.Len() == 0 {
				continue
			}
			line := t.readBuf.String()
			t.readBuf.Reset()
			t.events <- Event{Kind: EventRX, When: time.Now(), Text: line, Payload: []byte(line)}
		default:
			_ = t.readBuf.WriteByte(b)
		}
	}
}
