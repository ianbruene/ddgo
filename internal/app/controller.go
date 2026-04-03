package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"example.com/cncui/internal/gcode"
	"example.com/cncui/internal/grbl"
	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
)

var ErrProgramActive = errors.New("program is running; manual controls are disabled")

type programRun struct {
	program gcode.Program
	rxCh    chan string
	cancel  context.CancelFunc
}

type Controller struct {
	mu        sync.RWMutex
	transport transport.Transport
	listPorts ports.ListFunc
	events    chan Event
	state     State
	loaded    gcode.Program
	run       *programRun
}

func NewController(t transport.Transport, listPorts ports.ListFunc) *Controller {
	c := &Controller{
		transport: t,
		listPorts: listPorts,
		events:    make(chan Event, 1024),
		state:     State{ProgramStatus: ProgramNotLoaded},
	}
	go c.runTransportEventBridge()
	return c
}

func (c *Controller) Events() <-chan Event {
	return c.events
}

func (c *Controller) Snapshot() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Controller) RefreshPorts(ctx context.Context) error {
	if c.listPorts == nil {
		err := errors.New("port lister is not configured")
		c.emitError(err)
		return err
	}
	list, err := c.listPorts(ctx)
	if err != nil {
		c.emitError(err)
		return err
	}
	c.events <- Event{Kind: EventPortsRefreshed, When: time.Now(), Ports: clonePorts(list), State: c.Snapshot()}
	return nil
}

func (c *Controller) Connect(ctx context.Context, cfg transport.PortConfig) error {
	if cfg.Name == "" {
		err := errors.New("port name is required")
		c.emitError(err)
		return err
	}
	if cfg.BaudRate <= 0 {
		err := errors.New("baud rate must be greater than zero")
		c.emitError(err)
		return err
	}
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	if err := c.transport.Open(ctx, cfg); err != nil {
		c.emitError(err)
		return err
	}

	c.mu.Lock()
	c.state.Connected = true
	c.state.PortName = cfg.Name
	c.state.LastError = ""
	state := c.state
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("connected to %s", cfg.Name)}
	return nil
}

func (c *Controller) Disconnect() error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	if err := c.transport.Close(); err != nil {
		c.emitError(err)
		return err
	}

	c.mu.Lock()
	c.state.Connected = false
	c.state.MachineState = ""
	state := c.state
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "disconnected"}
	return nil
}

func (c *Controller) SendConsoleLine(ctx context.Context, line string) error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	if err := c.transport.Write(ctx, transport.NewLineMessage(line)); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) Jog(ctx context.Context, axis string, delta float64, feed float64) error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	msg, err := grbl.BuildJog(axis, delta, feed)
	if err != nil {
		c.emitError(err)
		return err
	}
	if err := c.transport.Write(ctx, msg); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) Action(ctx context.Context, action grbl.Action) error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	msg, err := grbl.BuildAction(action)
	if err != nil {
		c.emitError(err)
		return err
	}
	if err := c.transport.Write(ctx, msg); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) LoadProgramFile(path string) error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	prog, err := gcode.LoadFile(path)
	if err != nil {
		c.emitError(err)
		return err
	}
	c.mu.Lock()
	c.loaded = prog
	c.state.ProgramPath = prog.Path
	c.state.ProgramName = prog.Name
	c.state.ProgramStatus = ProgramLoaded
	c.state.ProgramTotal = len(prog.Lines)
	c.state.ProgramComplete = 0
	c.state.LastError = ""
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("loaded program %s (%d lines)", prog.Name, len(prog.Lines))}
	return nil
}

func (c *Controller) StartProgram(ctx context.Context) error {
	c.mu.Lock()
	if c.run != nil || c.state.ProgramStatus.IsActive() {
		c.mu.Unlock()
		err := errors.New("program is already running")
		c.emitError(err)
		return err
	}
	if !c.state.Connected {
		c.mu.Unlock()
		err := errors.New("connect to a machine before starting a program")
		c.emitError(err)
		return err
	}
	if len(c.loaded.Lines) == 0 {
		c.mu.Unlock()
		err := errors.New("load a program before starting")
		c.emitError(err)
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &programRun{
		program: c.loaded,
		rxCh:    make(chan string, 64),
		cancel:  cancel,
	}
	c.run = run
	c.state.ProgramStatus = ProgramRunning
	c.state.ProgramComplete = 0
	c.state.LastError = ""
	state := c.state
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("started program %s", run.program.Name)}
	go c.runProgram(runCtx, run)
	return nil
}

func (c *Controller) PauseProgram(ctx context.Context) error {
	c.mu.Lock()
	if c.run == nil || c.state.ProgramStatus != ProgramRunning {
		c.mu.Unlock()
		err := errors.New("program is not running")
		c.emitError(err)
		return err
	}
	c.state.ProgramStatus = ProgramPaused
	state := c.state
	c.mu.Unlock()
	if err := c.writeProgramAction(ctx, grbl.ActionHold); err != nil {
		return err
	}
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "program paused"}
	return nil
}

func (c *Controller) ResumeProgram(ctx context.Context) error {
	c.mu.Lock()
	if c.run == nil || c.state.ProgramStatus != ProgramPaused {
		c.mu.Unlock()
		err := errors.New("program is not paused")
		c.emitError(err)
		return err
	}
	c.state.ProgramStatus = ProgramRunning
	state := c.state
	c.mu.Unlock()
	if err := c.writeProgramAction(ctx, grbl.ActionResume); err != nil {
		return err
	}
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "program resumed"}
	return nil
}

func (c *Controller) StopProgram(ctx context.Context) error {
	c.mu.Lock()
	run := c.run
	if run == nil {
		c.mu.Unlock()
		err := errors.New("program is not running")
		c.emitError(err)
		return err
	}
	c.run = nil
	c.state.ProgramStatus = ProgramStopped
	state := c.state
	c.mu.Unlock()

	run.cancel()
	var firstErr error
	if err := c.writeProgramAction(ctx, grbl.ActionHold); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := c.writeProgramAction(ctx, grbl.ActionSoftReset); err != nil && firstErr == nil {
		firstErr = err
	}
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "program stopped"}
	return firstErr
}

func (c *Controller) runProgram(ctx context.Context, run *programRun) {
	for idx, line := range run.program.Lines {
		if err := c.waitUntilRunnable(ctx, run); err != nil {
			return
		}
		msg := transport.NewLineMessage(line.Text)
		if err := c.transport.Write(ctx, msg); err != nil {
			c.finishProgramFailure(run, fmt.Errorf("send line %d: %w", line.Number, err))
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case rx := <-run.rxCh:
				status := classifyProgramResponse(rx)
				switch status {
				case responseIgnore:
					continue
				case responseOK:
					c.updateProgramProgress(run, idx+1)
					goto nextLine
				case responseFail:
					c.finishProgramFailure(run, fmt.Errorf("program failed at line %d: %s", line.Number, strings.TrimSpace(rx)))
					return
				}
			}
		}
	nextLine:
	}
	c.finishProgramSuccess(run)
}

func (c *Controller) waitUntilRunnable(ctx context.Context, run *programRun) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		c.mu.RLock()
		stillCurrent := c.run == run
		status := c.state.ProgramStatus
		c.mu.RUnlock()
		if !stillCurrent {
			return context.Canceled
		}
		if status != ProgramPaused {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *Controller) updateProgramProgress(run *programRun, complete int) {
	c.mu.Lock()
	if c.run != run {
		c.mu.Unlock()
		return
	}
	c.state.ProgramComplete = complete
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state}
}

func (c *Controller) finishProgramSuccess(run *programRun) {
	c.mu.Lock()
	if c.run != run {
		c.mu.Unlock()
		return
	}
	c.run = nil
	c.state.ProgramStatus = ProgramCompleted
	c.state.ProgramComplete = c.state.ProgramTotal
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("program %s completed", run.program.Name)}
}

func (c *Controller) finishProgramFailure(run *programRun, err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	if c.run == run {
		c.run = nil
		c.state.ProgramStatus = ProgramFailed
		c.state.LastError = err.Error()
	}
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "program failed"}
	c.events <- Event{Kind: EventError, When: time.Now(), Err: err, Text: err.Error(), State: state}
}

func (c *Controller) writeProgramAction(ctx context.Context, action grbl.Action) error {
	msg, err := grbl.BuildAction(action)
	if err != nil {
		c.emitError(err)
		return err
	}
	if err := c.transport.Write(ctx, msg); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) runTransportEventBridge() {
	for ev := range c.transport.Events() {
		snapshot := c.Snapshot()
		switch ev.Kind {
		case transport.EventConnected, transport.EventDisconnected:
			continue
		case transport.EventTX:
			c.events <- Event{Kind: EventConsoleTX, When: ev.When, Text: ev.Text, State: snapshot, Raw: ev}
		case transport.EventRX:
			c.mu.Lock()
			if ms := grbl.ParseMachineState(ev.Text); ms != "" {
				c.state.MachineState = ms
			}
			if run := c.run; run != nil && isProgramResponse(ev.Text) {
				select {
				case run.rxCh <- ev.Text:
				default:
					go func(ch chan string, text string) { ch <- text }(run.rxCh, ev.Text)
				}
			}
			state := c.state
			c.mu.Unlock()
			c.events <- Event{Kind: EventConsoleRX, When: ev.When, Text: ev.Text, State: state, Raw: ev}
		case transport.EventError:
			c.emitError(ev.Err)
		}
	}
}

func (c *Controller) emitError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.state.LastError = err.Error()
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventError, When: time.Now(), Err: err, Text: err.Error(), State: state}
}

func isProgramResponse(line string) bool {
	switch classifyProgramResponse(line) {
	case responseOK, responseFail:
		return true
	default:
		return false
	}
}

type responseKind int

const (
	responseIgnore responseKind = iota
	responseOK
	responseFail
)

func classifyProgramResponse(line string) responseKind {
	line = strings.ToLower(strings.TrimSpace(line))
	switch {
	case line == "ok":
		return responseOK
	case strings.HasPrefix(line, "error"), strings.HasPrefix(line, "alarm"):
		return responseFail
	default:
		return responseIgnore
	}
}

func clonePorts(list []ports.Info) []ports.Info {
	out := make([]ports.Info, len(list))
	copy(out, list)
	return out
}
