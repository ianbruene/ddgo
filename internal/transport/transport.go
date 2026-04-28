package transport

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrNotOpen = errors.New("transport is not open")

type PortConfig struct {
	Name string
}

func DefaultPortConfig(name string) PortConfig {
	return PortConfig{
		Name: name,
	}
}

type Message struct {
	Payload []byte
	Display string
}

func NewLineMessage(line string) Message {
	normalized := strings.TrimRight(line, "\r\n")
	return Message{
		Payload: []byte(normalized + "\n"),
		Display: normalized,
	}
}

func NewRawMessage(payload []byte, display string) Message {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	return Message{Payload: cp, Display: display}
}

type EventKind string

const (
	EventConnected    EventKind = "connected"
	EventDisconnected EventKind = "disconnected"
	EventTX           EventKind = "tx"
	EventRX           EventKind = "rx"
	EventError        EventKind = "error"
)

type Event struct {
	Kind    EventKind
	When    time.Time
	Text    string
	Err     error
	Payload []byte
}

type Transport interface {
	Open(ctx context.Context, cfg PortConfig) error
	Close() error
	Write(ctx context.Context, msg Message) error
	Events() <-chan Event
}
