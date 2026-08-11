package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ianbruene/ddgo/internal/gcode"
	"github.com/ianbruene/ddgo/internal/macro"
)

func parseConsoleGCodeLine(text string) (gcode.Line, bool) {
	lines, err := gcode.Parse(text + "\n")
	if err != nil || len(lines) == 0 {
		return gcode.Line{}, false
	}
	line := lines[0]
	line.Number = 0
	return line, true
}

func commandCanBeApplicationMacro(line gcode.Line, engine *macro.Engine) bool {
	inv, ok := macro.ParseInvocation(line)
	if !ok {
		return false
	}
	if inv.Code == 110 {
		return true
	}
	if engine == nil {
		return false
	}
	return engine.Handles(inv.Code)
}

func (c *Controller) dispatchApplicationCommand(ctx context.Context, runtime macro.Runtime, line gcode.Line, engine *macro.Engine) (bool, error) {
	inv, ok := macro.ParseInvocation(line)
	if !ok {
		return false, nil
	}
	if inv.Code == 110 {
		return true, &macro.Error{LineNumber: line.Number, Code: inv.Code, Err: errors.New("unsupported legacy macro")}
	}
	if engine == nil {
		return false, nil
	}
	return engine.Dispatch(ctx, runtime, line)
}

func (c *Controller) executeApplicationCommand(ctx context.Context, runtime macro.Runtime, line gcode.Line, engine *macro.Engine) error {
	handled, err := c.dispatchApplicationCommand(ctx, runtime, line, engine)
	if err != nil {
		return err
	}
	if !handled {
		return fmt.Errorf("application command was not handled: %s", strings.TrimSpace(line.Text))
	}
	return nil
}

func (c *Controller) beginInteractiveSession() (*responseSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session := newResponseSession(make(chan string, 64))
	if err := c.acquireResponseOwnerLocked(responseOwnerInteractiveMacro, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (c *Controller) acquireResponseOwnerLocked(kind responseOwnerKind, session *responseSession) error {
	request := admissionManual
	if kind == responseOwnerProgram {
		request = admissionProgram
	} else if kind == responseOwnerInteractiveMacro {
		request = admissionInteractive
	}
	if err := c.admissionErrorLocked(request); err != nil {
		return err
	}
	c.responseOwner = responseOwner{kind: kind, session: session}
	return nil
}

// admissionErrorLocked is the single admission policy for lifecycle changes,
// response-owning commands, and realtime operations. c.mu must be held.
func (c *Controller) admissionErrorLocked(request admissionKind) error {
	if c.connectionTransition != connectionStable {
		return ErrConnectionTransition
	}
	if request == admissionConnect {
		if c.state.Connected {
			return ErrAlreadyConnected
		}
		return c.responseOwnerBusyErrorLocked(responseOwnerProgram)
	}
	if request == admissionDisconnect {
		if c.run != nil || c.state.ProgramStatus.IsActive() {
			return ErrProgramActive
		}
		return nil
	}
	if request == admissionRealtime {
		return nil
	}
	requested := responseOwnerManualLine
	if request == admissionProgram {
		requested = responseOwnerProgram
	} else if request == admissionInteractive {
		requested = responseOwnerInteractiveMacro
	}
	return c.responseOwnerBusyErrorLocked(requested)
}

func (c *Controller) responseOwnerBusyErrorLocked(requested responseOwnerKind) error {
	switch c.responseOwner.kind {
	case responseOwnerProgram:
		return ErrProgramActive
	case responseOwnerInteractiveMacro:
		return ErrInteractiveCommandActive
	case responseOwnerManualLine:
		if requested == responseOwnerProgram {
			return ErrProgramActive
		}
		return ErrInteractiveCommandActive
	default:
		if c.run != nil || c.state.ProgramStatus.IsActive() {
			return ErrProgramActive
		}
		return nil
	}
}

func (c *Controller) releaseResponseOwnerLocked(kind responseOwnerKind, session *responseSession) bool {
	if c.responseOwner.kind != kind || c.responseOwner.session != session {
		return false
	}
	c.responseOwner = responseOwner{}
	return true
}

func (c *Controller) terminateResponseOwnerLocked(owner responseOwner, cause error) bool {
	if owner.kind == responseOwnerNone || c.responseOwner != owner {
		return false
	}
	c.responseOwner = responseOwner{}
	if cause == nil {
		cause = ErrCommandSessionCanceled
	}
	if owner.session != nil {
		owner.session.cancel(cause)
	}
	return true
}

func newResponseSession(rxCh chan string) *responseSession {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &responseSession{rxCh: rxCh, ctx: ctx, cancel: cancel}
}

func (s *responseSession) Done() <-chan struct{} {
	if s == nil || s.ctx == nil {
		return nil
	}
	return s.ctx.Done()
}

func (s *responseSession) Err() error {
	if s == nil || s.ctx == nil {
		return nil
	}
	if cause := context.Cause(s.ctx); cause != nil {
		return cause
	}
	return s.ctx.Err()
}

func (c *Controller) terminateInteractiveSessionLocked(session *responseSession, cause error) bool {
	return c.terminateResponseOwnerLocked(responseOwner{kind: responseOwnerInteractiveMacro, session: session}, cause)
}

func (c *Controller) endInteractiveSession(session *responseSession, cause error) {
	c.mu.Lock()
	c.terminateResponseOwnerLocked(responseOwner{kind: responseOwnerInteractiveMacro, session: session}, cause)
	c.mu.Unlock()
}

func (c *Controller) runtimeForSession(session *responseSession) *commandRuntime {
	return &commandRuntime{controller: c, session: session}
}

type commandRuntime struct {
	controller *Controller
	session    *responseSession
}

func (r *commandRuntime) SendLineAndWaitOK(ctx context.Context, line string) error {
	return r.sendLineAndWaitOK(ctx, line, 0)
}
func (r *commandRuntime) SendLineCollectingResponses(ctx context.Context, line string) ([]string, error) {
	return r.sendLineCollectingResponses(ctx, line)
}
func (r *commandRuntime) ReadWCSOffsets(ctx context.Context) (macro.WCSOffsets, error) {
	lines, err := r.SendLineCollectingResponses(ctx, "$#")
	if err != nil {
		return nil, err
	}
	return macro.ParseWCSOffsetsResponse(lines)
}
func (r *commandRuntime) WriteWCSOffset(ctx context.Context, wcs macro.WCS, axis macro.Axis, value float64) error {
	line, err := macro.BuildWCSWrite(wcs, axis, value)
	if err != nil {
		return err
	}
	return r.SendLineAndWaitOK(ctx, line)
}
func (r *commandRuntime) CurrentMachinePosition() (macro.Point, bool) {
	return r.controller.CurrentMachinePosition()
}
func (r *commandRuntime) CurrentWorkPosition() (macro.Point, bool) {
	return r.controller.CurrentWorkPosition()
}
func (r *commandRuntime) LastProbePoint() (macro.Point, bool) { return r.controller.LastProbePoint() }
func (r *commandRuntime) RunProbe(ctx context.Context, args string) (macro.Point, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return macro.Point{}, errors.New("missing probe command")
	}
	lines, err := r.SendLineCollectingResponses(ctx, args)
	if err != nil {
		return macro.Point{}, err
	}
	return r.controller.probePointFromResponses(lines)
}
func (r *commandRuntime) Variables() *macro.VariableStore { return r.controller.variables }
func (r *commandRuntime) Contour() *macro.ContourState    { return r.controller.contour }

func (run *programRun) responseSession() *responseSession {
	if run.session == nil {
		run.session = newResponseSession(run.rxCh)
		run.session.queryRxCh = run.queryRxCh
	}
	if run.session.rxCh == nil {
		run.session.rxCh = run.rxCh
	}
	if run.rxCh == nil {
		run.rxCh = run.session.rxCh
	}
	if run.queryRxCh != nil {
		run.session.queryRxCh = run.queryRxCh
	}
	return run.session
}

func (c *Controller) runtimeForProgramRun(run *programRun) *commandRuntime {
	return c.runtimeForSession(run.responseSession())
}

func (c *Controller) sendLineCollectingResponses(ctx context.Context, run *programRun, line string) ([]string, error) {
	return c.runtimeForProgramRun(run).sendLineCollectingResponses(ctx, line)
}
