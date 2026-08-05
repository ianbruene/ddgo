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
	if c.run != nil || c.state.ProgramStatus.IsActive() {
		return nil, ErrProgramActive
	}
	if c.interactiveSession != nil {
		return nil, ErrInteractiveCommandActive
	}
	session := &responseSession{rxCh: make(chan string, 64)}
	c.interactiveSession = session
	return session, nil
}

func (c *Controller) endInteractiveSession(session *responseSession) {
	c.mu.Lock()
	if c.interactiveSession == session {
		c.interactiveSession = nil
	}
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
		run.session = &responseSession{rxCh: run.rxCh, queryRxCh: run.queryRxCh}
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
