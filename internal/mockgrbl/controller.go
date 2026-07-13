package mockgrbl

import (
	"fmt"
	"math"
	"strings"
	"sync"
)

type queuedMove struct {
	line string
	move Move
}
type Controller struct {
	mu                         sync.Mutex
	fw                         FirmwareProfile
	mach                       MachineProfile
	clock                      Clock
	state                      State
	pos                        [3]float64
	active                     *Move
	queue                      []queuedMove
	rx                         []byte
	logs                       []LogEntry
	commands                   []LogEntry
	responses                  []LogEntry
	events                     []LogEntry
	lastCmd, lastResp, lastErr string
}

func NewController(fw FirmwareProfile, mach MachineProfile, clock Clock) *Controller {
	if fw.Name == "" {
		fw = DefaultFirmwareProfile()
	}
	if mach.Name == "" {
		mach = DefaultMachineProfile()
	}
	if mach.PlannerQueueCapacity == 0 {
		mach.PlannerQueueCapacity = fw.PlannerBlockCapacity
	}
	if mach.SerialRXCapacity == 0 {
		mach.SerialRXCapacity = fw.SerialRXCapacity
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Controller{fw: fw, mach: mach, clock: clock, state: StateIdle, pos: mach.InitialPosition}
}
func (c *Controller) Connect() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.emit(c.fw.StartupBanner())
}
func (c *Controller) log(kind, text string) {
	e := LogEntry{c.clock.Now(), kind, text}
	c.logs = append(c.logs, e)
	c.events = append(c.events, e)
	if kind == "command" {
		c.commands = append(c.commands, e)
	}
	if kind == "response" {
		c.responses = append(c.responses, e)
	}
}
func (c *Controller) emit(s string) []string {
	c.lastResp = strings.TrimSpace(s)
	c.log("response", c.lastResp)
	return []string{s}
}
func (c *Controller) ProcessBytes(bs []byte) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range bs {
		c.reconcile()
		switch b {
		case c.fw.StatusByte:
			out = append(out, c.statusLine())
		case c.fw.JogCancelByte:
			c.cancelJog()
		case c.fw.FeedHoldByte:
			if c.state == StateJog {
				c.cancelJog()
			} else {
				c.setState(StateHold)
			}
		case c.fw.CycleStartByte:
			if c.state == StateHold {
				c.setState(StateIdle)
				c.startNext()
			}
		case c.fw.SoftResetByte, c.fw.AlternateResetByte:
			out = append(out, c.resetLocked()...)
		case '\n', '\r':
			line := string(c.rx)
			c.rx = c.rx[:0]
			out = append(out, c.handleLine(line)...)
		default:
			c.rx = append(c.rx, b)
		}
	}
	return out
}
func (c *Controller) handleLine(raw string) []string {
	norm := NormalizeLine(raw)
	c.lastCmd = norm
	c.log("command", norm)
	if norm == "" {
		return c.emit(c.fw.OK())
	}
	if strings.HasPrefix(norm, "$J=") {
		return c.handleJog(norm)
	}
	switch norm {
	case "$X":
		c.setState(StateIdle)
		c.lastErr = ""
		return c.emit(c.fw.OK())
	case "$H":
		c.setState(StateHome)
		c.pos = c.mach.InitialPosition
		c.setState(StateIdle)
		return c.emit(c.fw.OK())
	case "$I":
		return c.emit(fmt.Sprintf("[VER:%s:%s Ghost Gunner mock]%s", c.fw.Version, c.fw.Name, c.fw.LineEnding) + c.fw.OK())
	case "$G":
		return c.emit("[GC:G0 G54 G17 G21 G90 G94 M5 M9 T0 F0 S0]" + c.fw.LineEnding + c.fw.OK())
	default:
		return c.errorLine(norm, "Unsupported", 20)
	}
}
func (c *Controller) handleJog(norm string) []string {
	if c.state == StateAlarm {
		return c.errorLine(norm, "Busy", 9)
	}
	mv, err := c.parseJog(norm)
	if err != nil {
		return c.errorLine(norm, err.Error(), 2)
	}
	if bad := c.limitAxis(mv.Target); bad != "" {
		c.lastErr = "ALARM:2"
		c.log("limit", bad)
		c.setState(StateAlarm)
		return c.emit(c.fw.Msg("Soft Lim") + c.fw.Alarm(2))
	}
	if c.active == nil {
		c.startMove(mv)
	} else if len(c.queue) < c.mach.PlannerQueueCapacity {
		c.queue = append(c.queue, queuedMove{norm, mv})
		c.log("queue", "enqueue "+norm)
	} else {
		return c.errorLine(norm, "Queue full", 24)
	}
	return c.emit(c.fw.OK())
}
func (c *Controller) parseJog(norm string) (Move, error) {
	body := strings.TrimPrefix(norm, "$J=")
	w := parseWords(body)
	feed := w['F']
	if feed <= 0 {
		feed = c.mach.DefaultFeed
	}
	target := c.pos
	abs := strings.Contains(body, "G53") && strings.Contains(body, "G90")
	rel := strings.Contains(body, "G91")
	axes := 0
	for i, a := range []byte{'X', 'Y', 'Z'} {
		if v, ok := w[a]; ok {
			axes++
			if abs {
				target[i] = v
			} else if rel {
				target[i] += v
			} else {
				return Move{}, fmt.Errorf("missing distance mode")
			}
		}
	}
	if axes != 1 {
		return Move{}, fmt.Errorf("one axis required")
	}
	dist := math.Sqrt(sq(target[0]-c.pos[0]) + sq(target[1]-c.pos[1]) + sq(target[2]-c.pos[2]))
	dur := dist / feed * 60
	if dur <= 0 {
		dur = 0
	}
	return Move{Original: norm, Kind: MoveJog, Start: c.pos, Target: target, StartTime: c.clock.Now(), Duration: dur, Feed: feed}, nil
}
func sq(f float64) float64 { return f * f }
func (c *Controller) limitAxis(p [3]float64) string {
	for i, n := range []string{"X", "Y", "Z"} {
		if p[i] > c.mach.Max[i]+1e-9 || p[i] < c.mach.Min[i]-1e-9 {
			return n
		}
	}
	return ""
}
func (c *Controller) startMove(m Move) {
	m.Start = c.pos
	m.StartTime = c.clock.Now()
	c.active = &m
	if m.Kind == MoveJog {
		c.setState(StateJog)
	} else {
		c.setState(StateRun)
	}
	c.log("motion_start", m.Original)
}
func (c *Controller) reconcile() {
	if c.active == nil {
		return
	}
	now := c.clock.Now()
	prog := 1.0
	if c.active.Duration > 0 {
		prog = now.Sub(c.active.StartTime).Seconds() / c.active.Duration
	}
	if prog >= 1 {
		c.pos = c.active.Target
		c.log("motion_complete", c.active.Original)
		c.active = nil
		c.setState(StateIdle)
		c.startNext()
		return
	}
	if prog < 0 {
		prog = 0
	}
	for i := 0; i < 3; i++ {
		c.pos[i] = c.active.Start[i] + prog*(c.active.Target[i]-c.active.Start[i])
	}
}
func (c *Controller) startNext() {
	if c.active != nil || len(c.queue) == 0 {
		return
	}
	q := c.queue[0]
	c.queue = c.queue[1:]
	c.startMove(q.move)
}
func (c *Controller) cancelJog() {
	c.reconcile()
	if c.active != nil && c.active.Kind == MoveJog {
		c.log("motion_cancel", c.active.Original)
		c.active = nil
	}
	c.queue = nil
	c.setState(StateIdle)
}
func (c *Controller) resetLocked() []string {
	c.active = nil
	c.queue = nil
	c.rx = nil
	c.setState(StateIdle)
	c.lastErr = "ALARM:3"
	c.log("reset", "reset")
	return c.emit(c.fw.Msg("reset") + c.fw.Alarm(3) + c.fw.StartupBanner())
}
func (c *Controller) errorLine(line, msg string, code int) []string {
	c.lastErr = fmt.Sprintf("error:%d", code)
	return c.emit(line + c.fw.LineEnding + c.fw.Msg(msg) + c.fw.Error(code))
}
func (c *Controller) setState(s State) {
	if c.state != s {
		c.log("state", string(c.state)+"->"+string(s))
		c.state = s
	}
}
func (c *Controller) statusLine() string {
	c.reconcile()
	free := c.mach.PlannerQueueCapacity - len(c.queue)
	if c.active != nil {
		free--
	}
	if free < 0 {
		free = 0
	}
	line := fmt.Sprintf("<%s|M:%.3f,%.3f,%.3f|B:%d,%d|L:%d|0000>%s", c.state, c.pos[0], c.pos[1], c.pos[2], free, c.mach.SerialRXCapacity-len(c.rx), 0, c.fw.LineEnding)
	c.lastResp = strings.TrimSpace(line)
	c.log("response", c.lastResp)
	return line
}
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconcile()
	free := c.mach.PlannerQueueCapacity - len(c.queue)
	if c.active != nil {
		free--
	}
	qs := []string{}
	for _, q := range c.queue {
		qs = append(qs, q.line)
	}
	var am *MoveSnapshot
	if c.active != nil {
		p := 0.0
		e := c.clock.Now().Sub(c.active.StartTime).Seconds()
		if c.active.Duration > 0 {
			p = e / c.active.Duration
		}
		if p > 1 {
			p = 1
		}
		m := *c.active
		am = &MoveSnapshot{Move: &m, ElapsedSeconds: e, Progress: p}
	}
	return Snapshot{State: c.state, MachinePosition: c.pos, ActiveMove: am, QueueCapacity: c.mach.PlannerQueueCapacity, QueuedCommandCount: len(c.queue), QueuedCommands: qs, FreePlannerBlocks: free, FreeRXBytes: c.mach.SerialRXCapacity - len(c.rx), LastCommand: c.lastCmd, LastResponse: c.lastResp, LastErrorAlarm: c.lastErr, ProfileName: c.fw.Name, ProfileVersion: c.fw.Version}
}
func (c *Controller) Commands() []LogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]LogEntry(nil), c.commands...)
}
func (c *Controller) Responses() []LogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]LogEntry(nil), c.responses...)
}
func (c *Controller) Events() []LogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]LogEntry(nil), c.events...)
}
func (c *Controller) Profile() any {
	return struct {
		Firmware FirmwareProfile `json:"firmware"`
		Machine  MachineProfile  `json:"machine"`
	}{c.fw, c.mach}
}
func (c *Controller) Reset() []string { c.mu.Lock(); defer c.mu.Unlock(); return c.resetLocked() }
func (c *Controller) HardLimit(axis string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = nil
	c.queue = nil
	c.setState(StateAlarm)
	c.lastErr = "ALARM:1"
	return c.emit(c.fw.Msg("Limit "+axis) + c.fw.Alarm(1))
}
