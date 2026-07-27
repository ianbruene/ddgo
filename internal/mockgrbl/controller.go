package mockgrbl

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
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
	wcsOffsets                 [6][3]float64
	active                     *Move
	queue                      []queuedMove
	rx                         []byte
	logs                       []LogEntry
	commands                   []LogEntry
	responses                  []LogEntry
	events                     []LogEntry
	lastCmd, lastResp, lastErr string
	rxOverflow                 bool
}

func NewController(fw FirmwareProfile, mach MachineProfile, clock Clock) *Controller {
	if fw.Name == "" {
		fw = DefaultFirmwareProfile()
	}
	if fw.JogLimitMessage == "" {
		fw.JogLimitMessage = "jogLIM"
	}
	if fw.JogLimitErrorCode == 0 {
		fw.JogLimitErrorCode = 15
	}
	if fw.InvalidJogMessage == "" {
		fw.InvalidJogMessage = "jogINV"
	}
	if fw.InvalidJogErrorCode == 0 {
		fw.InvalidJogErrorCode = 16
	}
	if fw.LineOverflowMessage == "" {
		fw.LineOverflowMessage = "2long"
	}
	if fw.LineOverflowErrorCode == 0 {
		fw.LineOverflowErrorCode = 14
	}
	if fw.BuildDate == "" {
		fw.BuildDate = "20240619"
	}
	if fw.GGRevision == "" {
		fw.GGRevision = "3A"
	}
	if fw.PCBRevision == "" {
		fw.PCBRevision = "3A"
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
	c.logAt(kind, text, c.clock.Now())
}
func (c *Controller) logAt(kind, text string, t time.Time) {
	e := LogEntry{t, kind, text}
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
	c.logResponseLines(s)
	return []string{s}
}
func (c *Controller) logResponseLines(s string) {
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			c.log("response", line)
		}
	}
}
func (c *Controller) logRealtimeCommand(name string) {
	c.lastCmd = name
	c.log("command", name)
	c.log("realtime", name)
}
func (c *Controller) ProcessBytes(bs []byte) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range bs {
		c.reconcile()
		switch b {
		case c.fw.StatusByte:
			c.logRealtimeCommand("?")
			out = append(out, c.statusLine())
		case c.fw.JogCancelByte:
			c.logRealtimeCommand("Jog Cancel")
			c.cancelJog()
		case c.fw.FeedHoldByte:
			c.logRealtimeCommand("!")
			if c.state == StateJog {
				c.cancelJog()
			} else {
				c.setState(StateHold)
			}
		case c.fw.CycleStartByte:
			c.logRealtimeCommand("~")
			if c.state == StateHold {
				c.setState(StateIdle)
				c.startNext()
			}
		case c.fw.SoftResetByte:
			c.logRealtimeCommand("Ctrl-X")
			out = append(out, c.resetLocked()...)
		case c.fw.AlternateResetByte:
			c.logRealtimeCommand("|")
			out = append(out, c.resetLocked()...)
		case '\n', '\r':
			if c.rxOverflow {
				line := NormalizeLine(string(c.rx))
				c.rx = c.rx[:0]
				c.rxOverflow = false
				// This models GrblDD's line-length-exceeded path for an overlong buffered line.
				// Exact RX-ring overflow behavior should be confirmed with a hardware transcript.
				out = append(out, c.errorLineMaybeEcho(line, c.fw.LineOverflowMessage, c.fw.LineOverflowErrorCode)...)
				continue
			}
			line := string(c.rx)
			c.rx = c.rx[:0]
			out = append(out, c.handleLine(line)...)
		default:
			if b > 0x7f {
				c.logRealtimeCommand(fmt.Sprintf("Ignored Realtime 0x%02X", b))
				c.log("realtime_ignored", fmt.Sprintf("0x%02X", b))
				continue
			}
			if c.rxOverflow || len(c.rx) >= c.mach.SerialRXCapacity {
				c.rxOverflow = true
				continue
			}
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
	if strings.HasPrefix(norm, "G10L2") {
		return c.handleWCSWrite(norm)
	}
	if strings.HasPrefix(norm, "G38.2") {
		return c.handleProbe(norm)
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
		return c.emit(c.fw.BuildInfo() + c.fw.LineEnding + c.fw.OK())
	case "$G":
		return c.emit("[GC:G0 G54 G17 G21 G90 G94 M5 M9 T0 F0 S0]" + c.fw.LineEnding + c.fw.OK())
	case "$$":
		return c.settingsResponse()
	case "$#":
		return c.emit(c.wcsOffsetsResponse())
	default:
		return c.errorLine(norm, "Unsupported", 20)
	}
}

func (c *Controller) handleProbe(norm string) []string {
	const prefix = "G38.2"
	body := strings.TrimPrefix(norm, prefix)
	if len(body) < 4 || (body[0] != 'X' && body[0] != 'Y' && body[0] != 'Z') {
		return c.errorLine(norm, "Probe unsupported", 20)
	}

	feedAt := strings.IndexByte(body[1:], 'F')
	if feedAt < 0 {
		return c.errorLine(norm, "Probe unsupported", 20)
	}
	feedAt++
	if strings.Contains(body[feedAt+1:], "F") {
		return c.errorLine(norm, "Probe unsupported", 20)
	}
	target, targetErr := strconv.ParseFloat(body[1:feedAt], 64)
	feed, feedErr := strconv.ParseFloat(body[feedAt+1:], 64)
	if targetErr != nil || feedErr != nil || math.IsNaN(target) || math.IsInf(target, 0) ||
		math.IsNaN(feed) || math.IsInf(feed, 0) || feed <= 0 {
		return c.errorLine(norm, "Probe unsupported", 20)
	}

	point := c.pos
	axisIndex := int(body[0] - 'X')
	if body[0] == 'Z' {
		axisIndex = 2
	}
	point[axisIndex] = target
	contact := map[byte]float64{'X': -1, 'Y': -2, 'Z': -3.5}[body[0]]
	success := target <= contact
	if success {
		point[axisIndex] = contact
	}
	return c.probeResponse(point, success)
}

func (c *Controller) probeResponse(point [3]float64, success bool) []string {
	successFlag := 0
	if success {
		successFlag = 1
		c.pos = point
	}
	line := fmt.Sprintf("[PRB:%.3f,%.3f,%.3f:%d]", point[0], point[1], point[2], successFlag)
	return c.emit(line + c.fw.LineEnding + c.fw.OK())
}

func (c *Controller) handleWCSWrite(norm string) []string {
	const prefix = "G10L2P"
	if !strings.HasPrefix(norm, prefix) {
		return c.errorLine(norm, "WCS write unsupported", 20)
	}

	rest := strings.TrimPrefix(norm, prefix)
	parameterEnd := 0
	for parameterEnd < len(rest) && rest[parameterEnd] >= '0' && rest[parameterEnd] <= '9' {
		parameterEnd++
	}
	if parameterEnd == 0 {
		return c.errorLine(norm, "WCS write unsupported", 20)
	}
	parameter, err := strconv.Atoi(rest[:parameterEnd])
	if err != nil || parameter < 1 || parameter > 6 || parameterEnd >= len(rest) {
		return c.errorLine(norm, "WCS write unsupported", 20)
	}

	axis := rest[parameterEnd]
	value := rest[parameterEnd+1:]
	if (axis != 'X' && axis != 'Y' && axis != 'Z') || value == "" {
		return c.errorLine(norm, "WCS write unsupported", 20)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return c.errorLine(norm, "WCS write unsupported", 20)
	}
	axisIndex := map[byte]int{'X': 0, 'Y': 1, 'Z': 2}[axis]
	c.wcsOffsets[parameter-1][axisIndex] = parsed

	return c.emit(c.fw.OK())
}
func (c *Controller) wcsOffsetsResponse() string {
	lines := []string{"[MSG:wcs-dump]"}
	for i, offset := range c.wcsOffsets {
		lines = append(lines, fmt.Sprintf("[G%d:%.3f,%.3f,%.3f]", 54+i, offset[0], offset[1], offset[2]))
	}
	lines = append(lines, c.fw.OK())
	return strings.Join(lines, c.fw.LineEnding)
}

func (c *Controller) settingsResponse() []string {
	lines := []string{
		"$0=10", "$1=25", "$10=31", "$11=0.010", "$12=0.002", "$13=0",
		"$20=0", "$21=0", "$22=0", "$100=40.000", "$101=40.000", "$102=160.000",
		"$130=86.500", "$131=241.500", "$132=78.500", "ok",
	}
	return c.emit(strings.Join(lines, c.fw.LineEnding) + c.fw.LineEnding)
}

func (c *Controller) handleJog(norm string) []string {
	if c.state == StateAlarm {
		return c.errorLine(norm, "Busy", 9)
	}
	base := c.plannerBasePositionLocked()
	mv, rel, err := c.parseJog(norm, base)
	if err != nil {
		return c.errorLine(norm, err.Error(), 2)
	}
	if rel && (c.active != nil || len(c.queue) > 0) {
		return c.errorLine(norm, c.fw.InvalidJogMessage, c.fw.InvalidJogErrorCode)
	}
	if bad := c.limitAxis(mv.Target); bad != "" {
		c.log("limit", bad)
		return c.errorLine(norm, c.fw.JogLimitMessage, c.fw.JogLimitErrorCode)
	}
	if c.active == nil {
		c.startMove(mv)
	} else if c.freePlannerBlocksLocked() > 0 {
		c.queue = append(c.queue, queuedMove{norm, mv})
		c.log("queue", "enqueue "+norm)
	} else {
		return c.errorLine(norm, "Queue full", 24)
	}
	return c.emit(c.fw.OK())
}
func (c *Controller) plannerBasePositionLocked() [3]float64 {
	if len(c.queue) > 0 {
		return c.queue[len(c.queue)-1].move.Target
	}
	if c.active != nil {
		return c.active.Target
	}
	return c.pos
}

func (c *Controller) parseJog(norm string, base [3]float64) (Move, bool, error) {
	body := strings.TrimPrefix(norm, "$J=")
	w := parseWords(body)
	feed := w['F']
	if feed <= 0 {
		feed = c.mach.DefaultFeed
	}
	target := base
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
				return Move{}, rel, fmt.Errorf("missing distance mode")
			}
		}
	}
	if axes != 1 {
		return Move{}, rel, fmt.Errorf("one axis required")
	}
	dist := math.Sqrt(sq(target[0]-base[0]) + sq(target[1]-base[1]) + sq(target[2]-base[2]))
	dur := dist / feed * 60
	if dur <= 0 {
		dur = 0
	}
	return Move{Original: norm, Kind: MoveJog, Start: base, Target: target, StartTime: c.clock.Now(), Duration: dur, Feed: feed}, rel, nil
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
	c.startMoveAt(m, c.clock.Now())
}
func (c *Controller) startMoveAt(m Move, start time.Time) {
	m.Start = c.pos
	m.StartTime = start
	c.active = &m
	if m.Kind == MoveJog {
		c.setState(StateJog)
	} else {
		c.setState(StateRun)
	}
	c.logAt("motion_start", m.Original, start)
}
func (c *Controller) reconcile() {
	now := c.clock.Now()
	for c.active != nil {
		prog := 1.0
		elapsed := now.Sub(c.active.StartTime).Seconds()
		if c.active.Duration > 0 {
			prog = elapsed / c.active.Duration
		}
		if prog < 1 {
			if prog < 0 {
				prog = 0
			}
			for i := 0; i < 3; i++ {
				c.pos[i] = c.active.Start[i] + prog*(c.active.Target[i]-c.active.Start[i])
			}
			return
		}

		completion := c.active.StartTime.Add(time.Duration(c.active.Duration * float64(time.Second)))
		c.pos = c.active.Target
		c.logAt("motion_complete", c.active.Original, completion)
		c.active = nil
		c.setState(StateIdle)
		c.startNextAt(completion)
	}
}
func (c *Controller) startNext() {
	c.startNextAt(c.clock.Now())
}
func (c *Controller) startNextAt(start time.Time) {
	if c.active != nil || len(c.queue) == 0 {
		return
	}
	q := c.queue[0]
	c.queue = c.queue[1:]
	c.startMoveAt(q.move, start)
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
	c.rxOverflow = false
	c.setState(StateIdle)
	c.lastErr = "ALARM:3"
	c.log("reset", "reset")
	return c.emit(c.fw.Msg("reset") + c.fw.Alarm(3) + c.fw.StartupBanner())
}
func (c *Controller) errorLine(line, msg string, code int) []string {
	c.lastErr = fmt.Sprintf("error:%d", code)
	return c.emit(c.fw.Echo(line) + c.fw.Msg(msg) + c.fw.Error(code))
}
func (c *Controller) errorLineMaybeEcho(line, msg string, code int) []string {
	c.lastErr = fmt.Sprintf("error:%d", code)
	out := c.fw.Msg(msg) + c.fw.Error(code)
	if line != "" {
		out = c.fw.Echo(line) + out
	}
	return c.emit(out)
}
func (c *Controller) setState(s State) {
	if c.state != s {
		c.log("state", string(c.state)+"->"+string(s))
		c.state = s
	}
}
func (c *Controller) usedPlannerBlocksLocked() int {
	used := len(c.queue)
	if c.active != nil {
		used++
	}
	return used
}
func (c *Controller) freePlannerBlocksLocked() int {
	free := c.mach.PlannerQueueCapacity - c.usedPlannerBlocksLocked()
	if free < 0 {
		return 0
	}
	return free
}
func (c *Controller) freeRXBytesLocked() int {
	free := c.mach.SerialRXCapacity - len(c.rx)
	if free < 0 || c.rxOverflow {
		return 0
	}
	return free
}

func (c *Controller) statusLine() string {
	c.reconcile()
	free := c.freePlannerBlocksLocked()
	line := fmt.Sprintf("<%s|M:%.3f,%.3f,%.3f|B:%d,%d|L:%d|0000>%s", c.state, c.pos[0], c.pos[1], c.pos[2], free, c.freeRXBytesLocked(), 0, c.fw.LineEnding)
	c.lastResp = strings.TrimSpace(line)
	c.logResponseLines(line)
	return line
}
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconcile()
	free := c.freePlannerBlocksLocked()
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
	return Snapshot{State: c.state, MachinePosition: c.pos, ActiveMove: am, QueueCapacity: c.mach.PlannerQueueCapacity, QueuedCommandCount: len(c.queue), QueuedCommands: qs, FreePlannerBlocks: free, FreeRXBytes: c.freeRXBytesLocked(), LastCommand: c.lastCmd, LastResponse: c.lastResp, LastErrorAlarm: c.lastErr, ProfileName: c.fw.Name, ProfileVersion: c.fw.Version}
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

func (c *Controller) DiscardResponseLogs(responses []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	lines := responseLogLines(responses)
	if len(lines) == 0 {
		return
	}
	for _, line := range lines {
		c.responses = removeLastLogEntry(c.responses, "response", line)
		c.events = removeLastLogEntry(c.events, "response", line)
		c.logs = removeLastLogEntry(c.logs, "response", line)
	}
	c.lastResp = ""
	c.log("response_suppressed", strings.Join(lines, "\n"))
}

func responseLogLines(responses []string) []string {
	var lines []string
	for _, response := range responses {
		for _, line := range strings.Split(strings.ReplaceAll(response, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

func removeLastLogEntry(entries []LogEntry, kind, text string) []LogEntry {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == kind && entries[i].Text == text {
			copy(entries[i:], entries[i+1:])
			return entries[:len(entries)-1]
		}
	}
	return entries
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
