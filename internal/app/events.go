package app

import (
	"time"

	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

type EventKind string

type ProgramStatus string

const (
	EventStateChanged   EventKind = "state_changed"
	EventPortsRefreshed EventKind = "ports_refreshed"
	EventConsoleRX      EventKind = "console_rx"
	EventConsoleTX      EventKind = "console_tx"
	EventError          EventKind = "error"
)

const (
	ProgramNotLoaded ProgramStatus = "not_loaded"
	ProgramLoaded    ProgramStatus = "loaded"
	ProgramRunning   ProgramStatus = "running"
	ProgramPaused    ProgramStatus = "paused"
	ProgramStopped   ProgramStatus = "stopped"
	ProgramCompleted ProgramStatus = "completed"
	ProgramFailed    ProgramStatus = "failed"
)

func (s ProgramStatus) IsActive() bool {
	switch s {
	case ProgramRunning, ProgramPaused:
		return true
	default:
		return false
	}
}

type State struct {
	Connected       bool
	PortName        string
	MachineState    string
	LastError       string
	ProgramPath     string
	ProgramName     string
	ProgramStatus   ProgramStatus
	ProgramTotal    int
	ProgramComplete int
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
