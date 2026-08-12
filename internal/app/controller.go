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
var ErrInteractiveCommandActive = errors.New("interactive command is already active")
var ErrTransportDisconnected = errors.New("transport disconnected")
var ErrCommandSessionCanceled = errors.New("command session canceled")
var ErrControllerReset = errors.New("controller reset")
var ErrResponseBacklogFull = errors.New("response backlog full")
var ErrCommandSessionInvariant = errors.New("controller command session invariant violated")
var ErrConnectionTransition = errors.New("connection transition in progress")
var ErrAlreadyConnected = errors.New("controller is already connected")
var ErrConnectionInvariant = errors.New("controller connection invariant violated")
var ErrControllerIOActive = errors.New("controller transport operation in progress")

const defaultStatusPollInterval = 500 * time.Millisecond

type responseSession struct {
	rxCh      chan string
	queryRxCh chan string
	ctx       context.Context
	cancel    context.CancelCauseFunc
}

type responseOwnerKind int

const (
	responseOwnerNone responseOwnerKind = iota
	responseOwnerProgram
	responseOwnerInteractiveMacro
	responseOwnerManualLine
)

type responseOwner struct {
	kind    responseOwnerKind
	session *responseSession
}

type connectionTransition uint8

const (
	connectionStable connectionTransition = iota
	connectionConnecting
	connectionDisconnecting
)

type connectAttemptID uint64

type admissionKind uint8

const (
	admissionProgram admissionKind = iota
	admissionInteractive
	admissionManual
	admissionRealtime
	admissionConnect
	admissionDisconnect
)

type programRun struct {
	program   gcode.Program
	session   *responseSession
	rxCh      chan string
	queryRxCh chan string
	cancel    context.CancelFunc
}

type Controller struct {
	mu                                sync.RWMutex
	transport                         transport.Transport
	listPorts                         ports.ListFunc
	events                            chan Event
	state                             State
	loaded                            gcode.Program
	run                               *programRun
	responseOwner                     responseOwner
	connectionTransition              connectionTransition
	nextConnectAttempt                connectAttemptID
	activeConnectAttempt              connectAttemptID
	connectAttemptErr                 error
	realtimeWriteActive               bool
	realtimeWriteDone                 chan struct{}
	statusPollCancel                  context.CancelFunc
	statusPollDone                    chan struct{}
	statusPollInterval                time.Duration
	macroEngine                       *macro.Engine
	motionRewriter                    macro.MotionRewriter
	variables                         *macro.VariableStore
	contour                           *macro.ContourState
	lastProbe                         macro.Point
	hasLastProbe                      bool
	pendingQuietStatusReports         int
	suppressNextTransportDisconnected bool
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
	c.mu.Lock()
	if err := c.admissionErrorLocked(admissionConnect); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	attempt := c.beginConnectAttemptLocked()
	c.mu.Unlock()
	openErr := c.transport.Open(ctx, cfg)

	c.mu.Lock()
	if c.connectionTransition != connectionConnecting || c.activeConnectAttempt != attempt {
		c.finishConnectAttemptLocked(attempt)
		c.mu.Unlock()
		c.emitError(ErrConnectionInvariant)
		return ErrConnectionInvariant
	}
	if openErr != nil || c.connectAttemptErr != nil {
		err := openErr
		if c.connectAttemptErr != nil {
			err = c.connectAttemptErr
		}
		c.finishConnectAttemptLocked(attempt)
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	c.state.Connected = true
	c.state.PortName = cfg.Name
	c.state.LastError = ""
	c.finishConnectAttemptLocked(attempt)
	state := c.state
	c.startStatusPollingLocked()
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("connected to %s", cfg.Name)}
	return nil
}

func (c *Controller) beginConnectAttemptLocked() connectAttemptID {
	c.nextConnectAttempt++
	if c.nextConnectAttempt == 0 {
		c.nextConnectAttempt++
	}
	c.activeConnectAttempt = c.nextConnectAttempt
	c.connectAttemptErr = nil
	c.connectionTransition = connectionConnecting
	return c.activeConnectAttempt
}

func (c *Controller) finishConnectAttemptLocked(id connectAttemptID) bool {
	if id == 0 || c.activeConnectAttempt != id {
		return false
	}
	c.activeConnectAttempt = 0
	c.connectAttemptErr = nil
	c.connectionTransition = connectionStable
	return true
}

func (c *Controller) Disconnect() error {
	c.mu.Lock()
	if err := c.admissionErrorLocked(admissionDisconnect); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	c.connectionTransition = connectionDisconnecting
	writeDone := c.realtimeWriteDone
	c.terminateResponseOwnerLocked(c.responseOwner, ErrTransportDisconnected)
	c.suppressNextTransportDisconnected = c.state.Connected
	c.mu.Unlock()
	c.stopStatusPolling()
	if writeDone != nil {
		<-writeDone
	}
	if err := c.transport.Close(); err != nil {
		c.mu.Lock()
		c.suppressNextTransportDisconnected = false
		c.connectionTransition = connectionStable
		c.mu.Unlock()
		c.emitError(err)
		return err
	}

	c.mu.Lock()
	c.clearConnectionStateLocked()
	c.connectionTransition = connectionStable
	state := c.state
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "disconnected"}
	return nil
}

func (c *Controller) SendConsoleLine(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "?" {
		c.clearPendingQuietStatusReports()
		msg, err := grbl.BuildAction(grbl.ActionStatus)
		if err != nil {
			c.emitError(err)
			return err
		}
		return c.writeRealtimeMessage(ctx, msg)
	}
	line, parseOK := parseConsoleGCodeLine(text)
	if parseOK {
		c.mu.RLock()
		engine := c.macroEngine
		c.mu.RUnlock()
		if commandCanBeApplicationMacro(line, engine) {
			session, err := c.beginInteractiveSession()
			if err != nil {
				c.emitError(err)
				return err
			}
			runtime := c.runtimeForSession(session)
			err = c.executeApplicationCommand(ctx, runtime, line, engine)
			c.endInteractiveSession(session, err)
			if err != nil {
				c.emitError(err)
				return err
			}
			return nil
		}
	}

	return c.writeManualLine(ctx, text)
}

func (c *Controller) writeManualLine(ctx context.Context, text string) error {
	return c.writeManualResponseMessage(ctx, transport.NewLineMessage(text))
}

func (c *Controller) writeManualResponseMessage(ctx context.Context, msg transport.Message) error {
	session := newResponseSession(make(chan string, 1))
	c.mu.Lock()
	if err := c.acquireResponseOwnerLocked(responseOwnerManualLine, session); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	c.mu.Unlock()
	if err := c.transport.Write(ctx, msg); err != nil {
		c.mu.Lock()
		c.releaseResponseOwnerLocked(responseOwnerManualLine, session)
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) rejectManualWriteIfBusy() error {
	c.mu.RLock()
	err := c.admissionErrorLocked(admissionManual)
	c.mu.RUnlock()
	if err != nil {
		c.emitError(err)
	}
	return err
}

func (c *Controller) Jog(ctx context.Context, axis string, delta float64, feed float64) error {
	if err := c.rejectManualWriteIfBusy(); err != nil {
		return err
	}
	msg, err := grbl.BuildJog(axis, delta, feed)
	if err != nil {
		c.emitError(err)
		return err
	}
	return c.writeManualResponseMessage(ctx, msg)
}

func (c *Controller) JogTo(ctx context.Context, axis string, target float64, feed float64) error {
	if err := c.rejectManualWriteIfBusy(); err != nil {
		return err
	}
	msg, err := grbl.BuildMachineJog(axis, target, feed)
	if err != nil {
		c.emitError(err)
		return err
	}
	return c.writeManualResponseMessage(ctx, msg)
}

func (c *Controller) StopMotion(ctx context.Context) error {
	c.mu.Lock()
	programActive := c.state.ProgramStatus.IsActive() || c.run != nil
	if programActive {
		c.mu.Unlock()
		c.emitError(ErrProgramActive)
		return ErrProgramActive
	}
	if err := c.beginRealtimeWriteLocked(); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	c.mu.Unlock()
	defer c.endRealtimeWrite()
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
	c.mu.Lock()
	if !c.state.Connected {
		c.mu.Unlock()
		return nil
	}
	if err := c.beginRealtimeWriteLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	defer c.endRealtimeWrite()
	msg, err := grbl.BuildAction(grbl.ActionStatus)
	if err != nil {
		return err
	}
	msg.SuppressLog = true
	if err := c.transport.Write(ctx, msg); err != nil {
		if errors.Is(err, transport.ErrNotOpen) {
			return nil
		}
		c.emitError(err)
		return err
	}
	c.mu.Lock()
	c.pendingQuietStatusReports = 1
	c.mu.Unlock()
	return nil
}

func (c *Controller) Action(ctx context.Context, action grbl.Action) error {
	if actionProducesTerminalResponse(action) {
		msg, err := grbl.BuildAction(action)
		if err != nil {
			c.emitError(err)
			return err
		}
		return c.writeManualResponseMessage(ctx, msg)
	}
	if action == grbl.ActionStatus {
		c.mu.RLock()
		programActive := c.state.ProgramStatus.IsActive() || c.run != nil
		c.mu.RUnlock()
		if programActive {
			c.emitError(ErrProgramActive)
			return ErrProgramActive
		}
		c.clearPendingQuietStatusReports()
	}
	msg, err := grbl.BuildAction(action)
	if err != nil {
		c.emitError(err)
		return err
	}
	if action == grbl.ActionSoftReset {
		c.mu.Lock()
		err = c.beginRealtimeWriteLocked()
		if err == nil {
			err = c.prepareSoftResetLocked()
			if err != nil {
				c.endRealtimeWriteLocked()
			}
		}
		c.mu.Unlock()
		if err != nil {
			c.emitError(err)
			return err
		}
		// Cancellation deliberately precedes the write: reset RX must never be
		// routed to the command session whose controller state it invalidates.
		defer c.endRealtimeWrite()
		if err := c.transport.Write(ctx, msg); err != nil {
			c.emitError(err)
			return err
		}
		return nil
	}
	return c.writeRealtimeMessage(ctx, msg)
}

func (c *Controller) beginRealtimeWriteLocked() error {
	if err := c.admissionErrorLocked(admissionRealtime); err != nil {
		return err
	}
	c.realtimeWriteActive = true
	c.realtimeWriteDone = make(chan struct{})
	return nil
}

func (c *Controller) endRealtimeWriteLocked() {
	if !c.realtimeWriteActive {
		return
	}
	done := c.realtimeWriteDone
	c.realtimeWriteActive = false
	c.realtimeWriteDone = nil
	close(done)
}

func (c *Controller) endRealtimeWrite() {
	c.mu.Lock()
	c.endRealtimeWriteLocked()
	c.mu.Unlock()
}

func (c *Controller) writeRealtimeMessage(ctx context.Context, msg transport.Message) error {
	c.mu.Lock()
	if err := c.beginRealtimeWriteLocked(); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	c.mu.Unlock()
	defer c.endRealtimeWrite()
	if err := c.transport.Write(ctx, msg); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

// prepareSoftResetLocked cancels command ownership invalidated by a GRBL reset.
// Programs are excluded: StopProgram owns their cancellation and its established
// Hold/Soft Reset lifecycle, so Action must not create a competing stop path.
func (c *Controller) prepareSoftResetLocked() error {
	owner := c.responseOwner
	switch owner.kind {
	case responseOwnerNone:
		return nil
	case responseOwnerManualLine, responseOwnerInteractiveMacro:
		c.terminateResponseOwnerLocked(owner, ErrControllerReset)
		return nil
	case responseOwnerProgram:
		return ErrProgramActive
	default:
		return ErrCommandSessionInvariant
	}
}

// actionProducesTerminalResponse documents GRBL command ownership for UI/manual actions.
// Unlock ($X) and home ($H) are line commands that end in ok/error/alarm, so they
// reserve manual response ownership. Status (?) and realtime controls (!, ~, Ctrl-X,
// jog-cancel) do not emit terminal ok responses. Soft Reset is handled separately
// because, unlike the other realtime controls, it cancels controller command state.
func actionProducesTerminalResponse(action grbl.Action) bool {
	switch action {
	case grbl.ActionUnlock, grbl.ActionHome:
		return true
	default:
		return false
	}
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
	if err := c.admissionErrorLocked(admissionRealtime); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	if c.run != nil || c.state.ProgramStatus.IsActive() {
		c.mu.Unlock()
		err := errors.New("program is already running")
		c.emitError(err)
		return err
	}
	if err := c.admissionErrorLocked(admissionProgram); err != nil {
		c.mu.Unlock()
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
	run.session = newResponseSession(run.rxCh)
	if err := c.acquireResponseOwnerLocked(responseOwnerProgram, run.session); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
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
	if err := c.beginRealtimeWriteLocked(); err != nil {
		c.mu.Unlock()
		c.emitError(err)
		return err
	}
	c.run = nil
	c.releaseResponseOwnerLocked(responseOwnerProgram, run.responseSession())
	c.state.ProgramStatus = ProgramStopped
	state := c.state
	c.mu.Unlock()
	defer c.endRealtimeWrite()

	run.cancel()
	var firstErr error
	if err := c.writeProgramActionReserved(ctx, grbl.ActionHold); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := c.writeProgramActionReserved(ctx, grbl.ActionSoftReset); err != nil && firstErr == nil {
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

		runtime := c.runtimeForProgramRun(run)
		if handled, err := c.dispatchApplicationCommand(ctx, runtime, line, engine); err != nil {
			c.finishProgramFailure(run, err)
			return
		} else if handled {
			c.updateProgramProgress(run, idx+1)
			continue
		}

		outgoing := line.Text
		if rewriter != nil {
			rewritten, changed, err := rewriter.RewriteMotion(ctx, runtime, line)
			if err != nil {
				c.finishProgramFailure(run, fmt.Errorf("rewrite line %d: %w", line.Number, err))
				return
			}
			if changed {
				outgoing = rewritten
			}
		}

		if err := runtime.sendLineAndWaitOK(ctx, outgoing, line.Number); err != nil {
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
	return c.runtimeForProgramRun(run).sendLineAndWaitOK(ctx, line, 0)
}

func (c *Controller) SendLineCollectingResponses(ctx context.Context, line string) ([]string, error) {
	c.mu.RLock()
	run := c.run
	c.mu.RUnlock()
	if run == nil {
		return nil, errors.New("no active program run")
	}
	return c.runtimeForProgramRun(run).sendLineCollectingResponses(ctx, line)
}

func (r *commandRuntime) sendLineAndWaitOK(ctx context.Context, line string, sourceLine int) error {
	c := r.controller
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
		case <-r.session.Done():
			return r.session.Err()
		case rx := <-r.session.rxCh:
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

func (r *commandRuntime) sendLineCollectingResponses(ctx context.Context, line string) ([]string, error) {
	c := r.controller
	c.mu.Lock()
	if r.session.queryRxCh != nil {
		c.mu.Unlock()
		return nil, ErrProgramQueryActive
	}
	queryCh := make(chan string, 256)
	r.session.queryRxCh = queryCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if r.session.queryRxCh == queryCh {
			r.session.queryRxCh = nil
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
		case <-r.session.Done():
			return nil, r.session.Err()
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
	c.releaseResponseOwnerLocked(responseOwnerProgram, run.responseSession())
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
	var cancel context.CancelFunc
	c.mu.Lock()
	if run == nil || c.run != run {
		c.mu.Unlock()
		return
	}
	c.run = nil
	c.releaseResponseOwnerLocked(responseOwnerProgram, run.responseSession())
	c.state.ProgramStatus = ProgramFailed
	c.state.LastError = err.Error()
	c.contour.Disable()
	cancel = run.cancel
	state := c.state
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "program failed"}
	c.events <- Event{Kind: EventError, When: time.Now(), Err: err, Text: err.Error(), State: state}
}

func (c *Controller) handleTransportDisconnected() {
	c.stopStatusPolling()

	err := ErrTransportDisconnected
	var cancel context.CancelFunc
	c.mu.Lock()
	if c.connectionTransition == connectionConnecting && c.activeConnectAttempt != 0 {
		c.connectAttemptErr = err
	}
	run := c.run
	if run != nil {
		c.run = nil
		c.state.ProgramStatus = ProgramFailed
		c.state.LastError = err.Error()
		c.contour.Disable()
		cancel = run.cancel
	} else {
		c.state.LastError = ""
	}
	owner := c.responseOwner
	c.terminateResponseOwnerLocked(owner, err)
	changed := c.clearConnectionStateLocked()
	state := c.state
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if changed || run != nil || owner.kind != responseOwnerNone {
		c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: err.Error()}
	}
	if run != nil || owner.kind != responseOwnerNone {
		c.events <- Event{Kind: EventError, When: time.Now(), Err: err, Text: err.Error(), State: state}
	}
}

func (c *Controller) clearConnectionStateLocked() bool {
	changed := c.state.Connected ||
		c.state.MachineState != "" ||
		c.state.HasMachinePosition ||
		c.state.HasWorkPosition ||
		c.state.HasWorkCoordinateOffset ||
		c.state.HasFeedSpindle ||
		c.state.LastStatusRaw != ""
	c.state.Connected = false
	c.state.MachineState = ""
	c.state.HasMachinePosition = false
	c.state.HasWorkPosition = false
	c.state.WorkCoordinateOffset = [3]float64{}
	c.state.HasWorkCoordinateOffset = false
	c.state.HasFeedSpindle = false
	c.state.LastStatusRaw = ""
	return changed
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
	args = strings.TrimSpace(args)
	if args == "" {
		return macro.Point{}, errors.New("missing probe command")
	}
	lines, err := c.SendLineCollectingResponses(ctx, args)
	if err != nil {
		return macro.Point{}, err
	}
	return c.probePointFromResponses(lines)
}

func (c *Controller) probePointFromResponses(lines []string) (macro.Point, error) {
	for _, line := range lines {
		result, ok := grbl.ParseProbeResult(line)
		if !ok {
			continue
		}
		if !result.Success {
			return macro.Point{}, errors.New("probe did not contact")
		}
		point := macro.Point{X: result.Position[0], Y: result.Position[1], Z: result.Position[2]}
		c.mu.Lock()
		c.lastProbe = point
		c.hasLastProbe = true
		c.mu.Unlock()
		return point, nil
	}
	return macro.Point{}, errors.New("probe result not reported")
}

func (c *Controller) Variables() *macro.VariableStore { return c.variables }

func (c *Controller) Contour() *macro.ContourState { return c.contour }

func (c *Controller) clearPendingQuietStatusReports() {
	c.mu.Lock()
	c.pendingQuietStatusReports = 0
	c.mu.Unlock()
}

func (c *Controller) writeProgramAction(ctx context.Context, action grbl.Action) error {
	msg, err := grbl.BuildAction(action)
	if err != nil {
		c.emitError(err)
		return err
	}
	return c.writeRealtimeMessage(ctx, msg)
}

// writeProgramActionReserved writes within the reservation StopProgram acquired
// before releasing program ownership.
func (c *Controller) writeProgramActionReserved(ctx context.Context, action grbl.Action) error {
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
		case transport.EventConnected:
			continue
		case transport.EventDisconnected:
			if c.consumeExpectedTransportDisconnected() {
				continue
			}
			c.handleTransportDisconnected()
		case transport.EventTX:
			if !ev.SuppressLog {
				c.events <- Event{Kind: EventConsoleTX, When: ev.When, Text: ev.Text, State: snapshot, Raw: ev}
			}
		case transport.EventRX:
			c.mu.Lock()
			suppressRXLog := false
			if report, ok := grbl.ParseStatusReport(ev.Text); ok {
				if c.pendingQuietStatusReports > 0 {
					c.pendingQuietStatusReports--
					suppressRXLog = true
				}
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
				if report.HasWCO {
					c.state.WorkCoordinateOffset = report.WCO
					c.state.HasWorkCoordinateOffset = true
				}
				if report.HasFS {
					c.state.Feed = report.Feed
					c.state.Spindle = report.Spindle
					c.state.HasFeedSpindle = true
				}
			}
			overflowRun := c.deliverResponseLocked(ev.Text)
			state := c.state
			c.mu.Unlock()
			if overflowRun != nil {
				c.finishProgramFailure(overflowRun, errors.New("program response backlog full"))
			}
			if !suppressRXLog {
				c.events <- Event{Kind: EventConsoleRX, When: ev.When, Text: ev.Text, State: state, Raw: ev}
			}
		case transport.EventError:
			c.emitError(ev.Err)
		}
	}
}

func (c *Controller) consumeExpectedTransportDisconnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.suppressNextTransportDisconnected {
		return false
	}
	c.suppressNextTransportDisconnected = false
	return true
}

func (c *Controller) deliverResponseLocked(line string) *programRun {
	owner := c.responseOwner
	switch owner.kind {
	case responseOwnerNone:
		return nil
	case responseOwnerManualLine:
		if isProgramResponse(line) {
			c.releaseResponseOwnerLocked(responseOwnerManualLine, owner.session)
		}
		return nil
	case responseOwnerInteractiveMacro:
		if deliverToSession(owner.session, line) {
			return nil
		}
		c.terminateResponseOwnerLocked(owner, ErrResponseBacklogFull)
		return nil
	case responseOwnerProgram:
		run := c.run
		if run == nil || run.responseSession() != owner.session {
			c.terminateResponseOwnerLocked(owner, ErrCommandSessionInvariant)
			c.state.ProgramStatus = ProgramFailed
			c.state.LastError = ErrCommandSessionInvariant.Error()
			return nil
		}
		if deliverToSession(owner.session, line) {
			return nil
		}
		return run
	default:
		c.responseOwner = responseOwner{}
		c.state.LastError = ErrCommandSessionInvariant.Error()
		return nil
	}
}

func deliverToSession(session *responseSession, line string) bool {
	if session.ctx != nil && session.ctx.Err() != nil {
		return true
	}
	if session.queryRxCh != nil {
		select {
		case session.queryRxCh <- line:
			return true
		default:
			return false
		}
	}
	if !isProgramResponse(line) {
		return true
	}
	select {
	case session.rxCh <- line:
		return true
	default:
		return false
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
