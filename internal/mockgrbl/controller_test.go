package mockgrbl

import (
	"strings"
	"testing"
	"time"
)

func testCtl() (*Controller, *ManualClock) {
	clk := &ManualClock{T: time.Unix(0, 0)}
	return NewController(DefaultFirmwareProfile(), DefaultMachineProfile(), clk), clk
}
func joined(v []string) string { return strings.Join(v, "") }
func TestStartupBlankNormalize(t *testing.T) {
	c, _ := testCtl()
	if got := joined(c.Connect()); got != "\r\nGrbl 1.1g [help:'$']\r\n" {
		t.Fatal(got)
	}
	if got := joined(c.ProcessBytes([]byte(" (x) ; y\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	c.ProcessBytes([]byte(" $j = g53 g90 x-1 f60 (hi)\n"))
	if c.Snapshot().LastCommand != "$J=G53G90X-1F60" {
		t.Fatalf("norm %q", c.Snapshot().LastCommand)
	}
}
func TestJogStatusCancel(t *testing.T) {
	c, clk := testCtl()
	if got := joined(c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	clk.Advance(5 * time.Second)
	st := joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(st, "<Jog|M:-5.000,0.000,0.000|") {
		t.Fatal(st)
	}
	c.ProcessBytes([]byte{0x85})
	p := c.Snapshot().MachinePosition[0]
	clk.Advance(10 * time.Second)
	st = joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(st, "<Idle|M:") {
		t.Fatal(st)
	}
	if c.Snapshot().MachinePosition[0] != p {
		t.Fatal("moved after cancel")
	}
}
func TestEndpointNaturalCompleteAndLimit(t *testing.T) {
	c, clk := testCtl()
	c.ProcessBytes([]byte("$J=G53 G90 X-86.5 F865\n"))
	clk.Advance(7 * time.Second)
	st := joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(st, "<Idle|M:-86.500") {
		t.Fatal(st)
	}
	out := joined(c.ProcessBytes([]byte("$J=G53 G90 X-86.501 F100\n")))
	if !strings.Contains(out, "[MSG:jogLIM]\r\nerror:") {
		t.Fatal(out)
	}
	snap := c.Snapshot()
	if snap.State != StateIdle || snap.ActiveMove != nil || snap.QueuedCommandCount != 0 {
		t.Fatalf("unexpected limit snapshot: %+v", snap)
	}
}
func TestRelativeJogIdleAndQueuedSemantics(t *testing.T) {
	c, clk := testCtl()
	if got := joined(c.ProcessBytes([]byte("$J=G91 X-10 F60\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	clk.Advance(5 * time.Second)
	if got := c.Snapshot().MachinePosition; got[0] != -5 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("position = %v", got)
	}

	c, _ = testCtl()
	if got := joined(c.ProcessBytes([]byte("$J=G91 X-10 F60\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	out := joined(c.ProcessBytes([]byte("$J=G91 X-10 F60\n")))
	if !strings.Contains(out, "[echo: $J=G91X-10F60]\r\n[MSG:jogINV]\r\nerror:16") {
		t.Fatal(out)
	}
	if snap := c.Snapshot(); snap.ActiveMove == nil || snap.QueuedCommandCount != 0 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	c, _ = testCtl()
	if got := joined(c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	if got := joined(c.ProcessBytes([]byte("$J=G53 G90 Y-10 F60\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	if snap := c.Snapshot(); snap.ActiveMove == nil || snap.QueuedCommandCount != 1 {
		t.Fatalf("unexpected absolute queued snapshot: %+v", snap)
	}
}

func TestRealtimeBypassesQueueResetB(t *testing.T) {
	c, clk := testCtl()
	c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))
	c.ProcessBytes([]byte("$J=G53 G90 Y-10 F60\n"))
	st := joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(st, "|B:13,") {
		t.Fatal(st)
	}
	c.ProcessBytes([]byte{0x85})
	if c.Snapshot().QueuedCommandCount != 0 || c.Snapshot().State != StateIdle {
		t.Fatal(c.Snapshot())
	}
	c.ProcessBytes([]byte("$J=G53 G90 Z-10 F60\n"))
	clk.Advance(time.Second)
	out := joined(c.ProcessBytes([]byte{0x18}))
	if !strings.Contains(out, "[MSG:reset]") || c.Snapshot().ActiveMove != nil {
		t.Fatal(out)
	}
}
func TestBuildInfoIsGrblDDShaped(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("$I\n")))
	if !strings.Contains(out, "[grbl:1.1g GG:") || !strings.Contains(out, "PCB:") || !strings.Contains(out, "YMD:20240619") {
		t.Fatal(out)
	}
	if !strings.HasSuffix(out, "ok\r\n") {
		t.Fatal(out)
	}
}

func TestMalformedAndHardLimit(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("G2 X1\n")))
	if !strings.Contains(out, "[echo: G2X1]\r\n[MSG:Unsupported]\r\nerror:20") {
		t.Fatal(out)
	}
	out = joined(c.HardLimit("X"))
	if out != "[MSG:Limit X]\r\nALARM:1\r\n" {
		t.Fatal(out)
	}
}

func TestReconcileConsumesElapsedAcrossQueuedMoves(t *testing.T) {
	c, clk := testCtl()
	c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))
	c.ProcessBytes([]byte("$J=G53 G90 Y-10 F60\n"))
	c.ProcessBytes([]byte("$J=G53 G90 Z-10 F60\n"))

	clk.Advance(50 * time.Second)
	st := joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(st, "<Idle|M:-10.000,-10.000,-10.000|") {
		t.Fatal(st)
	}
	snap := c.Snapshot()
	if snap.State != StateIdle || snap.ActiveMove != nil || snap.QueuedCommandCount != 0 {
		t.Fatalf("unexpected snapshot after reconciliation: %+v", snap)
	}
	if snap.MachinePosition != [3]float64{-10, -10, -10} {
		t.Fatalf("position = %v", snap.MachinePosition)
	}
}

func TestReconcileCarriesPartialElapsedIntoNextMove(t *testing.T) {
	c, clk := testCtl()
	c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))
	c.ProcessBytes([]byte("$J=G53 G90 Y-10 F60\n"))

	clk.Advance(15 * time.Second)
	snap := c.Snapshot()
	if snap.State != StateJog || snap.ActiveMove == nil || snap.QueuedCommandCount != 0 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if got := snap.MachinePosition; got[0] != -10 || got[1] >= 0 || got[1] <= -10 || got[2] != 0 {
		t.Fatalf("position = %v, want X held at first endpoint and Y midway through second move", got)
	}
}

func TestReconcileMotionLogsUseSimulatedTimes(t *testing.T) {
	c, clk := testCtl()
	c.ProcessBytes([]byte("$J=G53 G90 X-1 F60\n"))
	c.ProcessBytes([]byte("$J=G53 G90 Y-1 F60\n"))
	observation := clk.Now().Add(3 * time.Second)
	clk.T = observation
	c.Snapshot()

	var starts, completes []LogEntry
	for _, e := range c.Events() {
		switch e.Kind {
		case "motion_start":
			starts = append(starts, e)
		case "motion_complete":
			completes = append(completes, e)
		}
	}
	if len(starts) != 2 || len(completes) != 2 {
		t.Fatalf("starts=%+v completes=%+v", starts, completes)
	}
	if !starts[1].Time.Equal(completes[0].Time) {
		t.Fatalf("second start time = %s, first complete time = %s", starts[1].Time, completes[0].Time)
	}
	if completes[1].Time.After(observation) {
		t.Fatalf("final complete time = %s after observation %s", completes[1].Time, observation)
	}
}

func TestPlannerCapacityCountsActiveMove(t *testing.T) {
	fw := DefaultFirmwareProfile()
	mach := DefaultMachineProfile()
	mach.PlannerQueueCapacity = 2
	clk := &ManualClock{T: time.Unix(0, 0)}
	c := NewController(fw, mach, clk)

	if got := joined(c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	if got := joined(c.ProcessBytes([]byte("$J=G53 G90 Y-10 F60\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	out := joined(c.ProcessBytes([]byte("$J=G53 G90 Z-10 F60\n")))
	if !strings.Contains(out, "[MSG:Queue full]\r\nerror:24") {
		t.Fatal(out)
	}
	snap := c.Snapshot()
	if snap.FreePlannerBlocks != 0 || snap.QueuedCommandCount != 1 || snap.ActiveMove == nil {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if st := joined(c.ProcessBytes([]byte("?"))); !strings.Contains(st, "|B:0,") {
		t.Fatal(st)
	}
}

func TestRXCapacityClampedAndOverflowHandled(t *testing.T) {
	fw := DefaultFirmwareProfile()
	mach := DefaultMachineProfile()
	mach.SerialRXCapacity = 3
	c := NewController(fw, mach, &ManualClock{T: time.Unix(0, 0)})

	c.ProcessBytes([]byte("abcd"))
	st := joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(st, "|B:15,0|") {
		t.Fatal(st)
	}

	out := joined(c.ProcessBytes([]byte("\n")))
	if !strings.Contains(out, "[echo: ABC]\r\n[MSG:2long]\r\nerror:14") {
		t.Fatal(out)
	}
	if strings.Contains(out, "[echo: ]") {
		t.Fatal(out)
	}
	if got := joined(c.ProcessBytes([]byte("$X\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
}

func TestRXOverflowWithoutBufferedLineOmitsEmptyEcho(t *testing.T) {
	fw := DefaultFirmwareProfile()
	mach := DefaultMachineProfile()
	mach.SerialRXCapacity = -1
	c := NewController(fw, mach, &ManualClock{T: time.Unix(0, 0)})

	out := joined(c.ProcessBytes([]byte("a\n")))
	if strings.Contains(out, "[echo: ]") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "[MSG:2long]\r\nerror:14") {
		t.Fatal(out)
	}
}

func TestRealtimeStatusDuringRXOverflow(t *testing.T) {
	fw := DefaultFirmwareProfile()
	mach := DefaultMachineProfile()
	mach.SerialRXCapacity = 3
	c := NewController(fw, mach, &ManualClock{T: time.Unix(0, 0)})

	c.ProcessBytes([]byte("abcd"))
	st := joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(st, "<Idle|M:0.000,0.000,0.000|B:15,0|") {
		t.Fatal(st)
	}
}

func TestUnknownExtendedRealtimeByteIsDiscarded(t *testing.T) {
	c, _ := testCtl()
	if out := joined(c.ProcessBytes([]byte{0x90})); out != "" {
		t.Fatalf("unknown realtime emitted response %q", out)
	}
	if got := joined(c.ProcessBytes([]byte("$X\n"))); got != "ok\r\n" {
		t.Fatal(got)
	}
	if snap := c.Snapshot(); snap.LastCommand != "$X" {
		t.Fatalf("last command = %q", snap.LastCommand)
	}
	found := false
	for _, e := range c.Events() {
		if e.Kind == "realtime_ignored" && e.Text == "0x90" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing ignored realtime event: %+v", c.Events())
	}
}

func TestRealtimeCommandsAreLogged(t *testing.T) {
	c, _ := testCtl()
	c.ProcessBytes([]byte("?"))
	c.ProcessBytes([]byte{0x85})

	commands := c.Commands()
	if !hasLog(commands, "command", "?") || !hasLog(commands, "command", "Jog Cancel") {
		t.Fatalf("missing realtime commands: %+v", commands)
	}
	if !hasLog(c.Events(), "realtime", "?") || !hasLog(c.Events(), "realtime", "Jog Cancel") {
		t.Fatalf("missing realtime events: %+v", c.Events())
	}
}

func TestResetResponsesAreLoggedAsSeparateLines(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte{0x18}))
	want := "[MSG:reset]\r\nALARM:3\r\n\r\nGrbl 1.1g [help:'$']\r\n"
	if out != want {
		t.Fatalf("reset serial output = %q, want %q", out, want)
	}
	responses := c.Responses()
	if !hasLog(responses, "response", "[MSG:reset]") || !hasLog(responses, "response", "ALARM:3") || !hasLog(responses, "response", "Grbl 1.1g [help:'$']") {
		t.Fatalf("responses not split: %+v", responses)
	}
}

func hasLog(entries []LogEntry, kind, text string) bool {
	for _, e := range entries {
		if e.Kind == kind && e.Text == text {
			return true
		}
	}
	return false
}
