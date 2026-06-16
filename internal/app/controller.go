package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ianbruene/ddgo/internal/gcode"
	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/macro"
	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

var ErrProgramActive = errors.New("program is running; manual controls are disabled")
var ErrProgramQueryActive = errors.New("program query is already active")

const defaultStatusPollInterval = 500 * time.Millisecond

type programRun struct {
	program   gcode.Program
	rxCh      chan string
	queryRxCh chan string
	cancel    context.CancelFunc
}

type Controller struct {
	mu                 sync.RWMutex
	transport          transport.Transport
	listPorts          ports.ListFunc
	events             chan Event
	state              State
	loaded             gcode.Program
	run                *programRun
	statusPollCancel   context.CancelFunc
	statusPollDone     chan struct{}
	statusPollInterval time.Duration
	macroEngine        *macro.Engine
	motionRewriter     macro.MotionRewriter
	variables          *macro.VariableStore
	contour            *macro.ContourState
	lastProbe          macro.Point
	hasLastProbe       bool
}

func NewController(t transport.Transport, listPorts ports.ListFunc) *Controller {
	c := &Controller{
		transport:          t,
		listPorts:          listPorts,
		events:             make(chan Event, 1024),
		state:              State{ProgramStatus: ProgramNotLoaded},
		statusPollInterval: defaultStatusPollInterval,
		macroEngine:        macro.NewDefaultEngine(),
		variables:          macro.NewVariableStore(),
		contour:            macro.NewContourState(),
	}
	go c.runTransportEventBridge()
	return c
}

func (c *Controller) Events() <-chan Event {
	return c.events
}

func (c *Controller) SetMacroEngine(engine *macro.Engine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.macroEngine = engine
}

func (c *Controller) SetMotionRewriter(rewriter macro.MotionRewriter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.motionRewriter = rewriter
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
	c.startStatusPollingLocked()
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("connected to %s", cfg.Name)}
	return nil
}

func (c *Controller) Disconnect() error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	c.stopStatusPolling()
	if err := c.transport.Close(); err != nil {
		c.emitError(err)
		return err
	}

	c.mu.Lock()
	c.state.Connected = false
	c.state.MachineState = ""
	c.state.HasMachinePosition = false
	c.state.HasWorkPosition = false
	c.state.HasFeedSpindle = false
	c.state.LastStatusRaw = ""
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

func (c *Controller) JogTo(ctx context.Context, axis string, target float64, feed float64) error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	msg, err := grbl.BuildMachineJog(axis, target, feed)
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

func (c *Controller) StopMotion(ctx context.Context) error {
	if c.Snapshot().ProgramStatus.IsActive() {
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	msg, err := grbl.BuildAction(grbl.ActionJogCancel)
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

func (c *Controller) startStatusPollingLocked() {
	if c.statusPollCancel != nil || !c.state.Connected {
		return
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.statusPollCancel = cancel
	c.statusPollDone = done
	interval := c.statusPollInterval
	go c.pollStatusLoop(pollCtx, done, interval)
}

func (c *Controller) stopStatusPolling() {
	c.mu.Lock()
	cancel := c.statusPollCancel
	done := c.statusPollDone
	c.statusPollCancel = nil
	c.statusPollDone = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (c *Controller) pollStatusLoop(ctx context.Context, done chan struct{}, interval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.writeStatusPoll(ctx); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
			}
		}
	}
}

func (c *Controller) writeStatusPoll(ctx context.Context) error {
	c.mu.RLock()
	connected := c.state.Connected
	c.mu.RUnlock()
	if !connected {
		return nil
	}
	msg, err := grbl.BuildAction(grbl.ActionStatus)
	if err != nil {
		return err
	}
	if err := c.transport.Write(ctx, msg); err != nil {
		if errors.Is(err, transport.ErrNotOpen) {
			return nil
		}
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
	runCtx, cancel := context.WithCancel(ctx)
	if err := runCtx.Err(); err != nil {
		cancel()
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	run := &programRun{
		program: c.loaded,
		rxCh:    make(chan string, 64),
		cancel:  cancel,
	}
	c.run = run
	c.state.ProgramStatus = ProgramRunning
	c.state.ProgramComplete = 0
	c.state.LastError = ""
	c.contour.Disable()
	state := c.state
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("started program %s", run.program.Name)}
	go c.runProgram(runCtx, run)
	return nil
}

func (c *Controller) PauseProgram(ctx context.Context) error {
	c.mu.Lock()
	run := c.run
	if run == nil || c.state.ProgramStatus != ProgramRunning {
		c.mu.Unlock()
		err := errors.New("program is not running")
		c.emitError(err)
		return err
	}
	c.mu.Unlock()
	if err := c.writeProgramAction(ctx, grbl.ActionHold); err != nil {
		return err
	}
	c.mu.Lock()
	if c.run != run || c.state.ProgramStatus != ProgramRunning {
		c.mu.Unlock()
		return nil
	}
	c.state.ProgramStatus = ProgramPaused
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "program paused"}
	return nil
}

func (c *Controller) ResumeProgram(ctx context.Context) error {
	c.mu.Lock()
	run := c.run
	if run == nil || c.state.ProgramStatus != ProgramPaused {
		c.mu.Unlock()
		err := errors.New("program is not paused")
		c.emitError(err)
		return err
	}
	c.mu.Unlock()
	if err := c.writeProgramAction(ctx, grbl.ActionResume); err != nil {
		return err
	}
	c.mu.Lock()
	if c.run != run || c.state.ProgramStatus != ProgramPaused {
		c.mu.Unlock()
		return nil
	}
	c.state.ProgramStatus = ProgramRunning
	state := c.state
	c.mu.Unlock()
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

		c.mu.RLock()
		engine := c.macroEngine
		rewriter := c.motionRewriter
		c.mu.RUnlock()

		if engine != nil {
			handled, err := engine.Dispatch(ctx, c, line)
			if err != nil {
				c.finishProgramFailure(run, err)
				return
			}
			if handled {
				c.updateProgramProgress(run, idx+1)
				continue
			}
		}

		outgoing := line.Text
		if rewriter != nil {
			rewritten, changed, err := rewriter.RewriteMotion(ctx, c, line)
			if err != nil {
				c.finishProgramFailure(run, fmt.Errorf("rewrite line %d: %w", line.Number, err))
				return
			}
			if changed {
				outgoing = rewritten
			}
		}

		if err := c.sendLineAndWaitOK(ctx, run, outgoing, line.Number); err != nil {
			c.finishProgramFailure(run, err)
			return
		}
		c.updateProgramProgress(run, idx+1)
	}
	c.finishProgramSuccess(run)
}

func (c *Controller) SendLineAndWaitOK(ctx context.Context, line string) error {
	c.mu.RLock()
	run := c.run
	c.mu.RUnlock()
	if run == nil {
		return errors.New("no active program run")
	}
	return c.sendLineAndWaitOK(ctx, run, line, 0)
}

func (c *Controller) SendLineCollectingResponses(ctx context.Context, line string) ([]string, error) {
	c.mu.RLock()
	run := c.run
	c.mu.RUnlock()
	if run == nil {
		return nil, errors.New("no active program run")
	}
	return c.sendLineCollectingResponses(ctx, run, line)
}

func (c *Controller) sendLineAndWaitOK(ctx context.Context, run *programRun, line string, sourceLine int) error {
	msg := transport.NewLineMessage(line)
	if err := c.transport.Write(ctx, msg); err != nil {
		if sourceLine > 0 {
			return fmt.Errorf("send line %d: %w", sourceLine, err)
		}
		return fmt.Errorf("send macro line: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case rx := <-run.rxCh:
			status := classifyProgramResponse(rx)
			switch status {
			case responseIgnore:
				continue
			case responseOK:
				return nil
			case responseFail:
				if sourceLine > 0 {
					return fmt.Errorf("program failed at line %d: %s", sourceLine, strings.TrimSpace(rx))
				}
				return fmt.Errorf("macro command failed: %s", strings.TrimSpace(rx))
			}
		}
	}
}

func (c *Controller) sendLineCollectingResponses(ctx context.Context, run *programRun, line string) ([]string, error) {
	c.mu.Lock()
	if c.run != run {
		c.mu.Unlock()
		return nil, context.Canceled
	}
	if run.queryRxCh != nil {
		c.mu.Unlock()
		return nil, ErrProgramQueryActive
	}
	queryCh := make(chan string, 256)
	run.queryRxCh = queryCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.run == run && run.queryRxCh == queryCh {
			run.queryRxCh = nil
		}
		c.mu.Unlock()
	}()

	msg := transport.NewLineMessage(line)
	if err := c.transport.Write(ctx, msg); err != nil {
		return nil, fmt.Errorf("send query line: %w", err)
	}
	var responses []string
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case rx := <-queryCh:
			switch classifyProgramResponse(rx) {
			case responseOK:
				return responses, nil
			case responseFail:
				return nil, fmt.Errorf("query command failed: %s", strings.TrimSpace(rx))
			case responseIgnore:
				responses = append(responses, rx)
			}
		}
	}
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
		c.contour.Disable()
	}
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "program failed"}
	c.events <- Event{Kind: EventError, When: time.Now(), Err: err, Text: err.Error(), State: state}
}

func (c *Controller) ReadWCSOffsets(ctx context.Context) (macro.WCSOffsets, error) {
	lines, err := c.SendLineCollectingResponses(ctx, "$#")
	if err != nil {
		return nil, err
	}
	return macro.ParseWCSOffsetsResponse(lines)
}

func (c *Controller) WriteWCSOffset(ctx context.Context, wcs macro.WCS, axis macro.Axis, value float64) error {
	line, err := macro.BuildWCSWrite(wcs, axis, value)
	if err != nil {
		return err
	}
	return c.SendLineAndWaitOK(ctx, line)
}

func (c *Controller) CurrentMachinePosition() (macro.Point, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.state.HasMachinePosition {
		return macro.Point{}, false
	}
	p := c.state.MachinePosition
	return macro.Point{X: p[0], Y: p[1], Z: p[2]}, true
}

func (c *Controller) CurrentWorkPosition() (macro.Point, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.state.HasWorkPosition {
		return macro.Point{}, false
	}
	p := c.state.WorkPosition
	return macro.Point{X: p[0], Y: p[1], Z: p[2]}, true
}

func (c *Controller) LastProbePoint() (macro.Point, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastProbe, c.hasLastProbe
}

func (c *Controller) RunProbe(ctx context.Context, args string) (macro.Point, error) {
	return macro.Point{}, errors.New("run probe is not available yet")
}

func (c *Controller) Variables() *macro.VariableStore { return c.variables }

func (c *Controller) Contour() *macro.ContourState { return c.contour }

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
			if report, ok := grbl.ParseStatusReport(ev.Text); ok {
				c.state.MachineState = report.State
				c.state.LastStatusRaw = report.Raw
				if report.HasMPos {
					c.state.MachinePosition = report.MPos
					c.state.HasMachinePosition = true
				}
				if report.HasWPos {
					c.state.WorkPosition = report.WPos
					c.state.HasWorkPosition = true
				}
				if report.HasFS {
					c.state.Feed = report.Feed
					c.state.Spindle = report.Spindle
					c.state.HasFeedSpindle = true
				}
			}
			overflowRun := c.deliverProgramResponseLocked(ev.Text)
			state := c.state
			c.mu.Unlock()
			if overflowRun != nil {
				c.finishProgramFailure(overflowRun, errors.New("program response backlog full"))
			}
			c.events <- Event{Kind: EventConsoleRX, When: ev.When, Text: ev.Text, State: state, Raw: ev}
		case transport.EventError:
			c.emitError(ev.Err)
		}
	}
}

func (c *Controller) deliverProgramResponseLocked(line string) *programRun {
	run := c.run
	if run == nil {
		return nil
	}
	if run.queryRxCh != nil {
		select {
		case run.queryRxCh <- line:
			return nil
		default:
			return run
		}
	}
	if !isProgramResponse(line) {
		return nil
	}
	select {
	case run.rxCh <- line:
		return nil
	default:
		return run
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
