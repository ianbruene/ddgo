package app

import (
	"time"

	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
)

type EventKind string

const (
	EventStateChanged   EventKind = "state_changed"
	EventPortsRefreshed EventKind = "ports_refreshed"
	EventConsoleRX      EventKind = "console_rx"
	EventConsoleTX      EventKind = "console_tx"
	EventError          EventKind = "error"
)

type State struct {
	Connected    bool
	PortName     string
	MachineState string
	LastError    string
}

type Event struct {
	Kind  EventKind
	When  time.Time
	Text  string
	Err   error
	State State
	Ports []ports.Info
	Raw   transport.Event
}
