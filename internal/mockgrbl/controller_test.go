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

func TestStatusReportDefaultUsesM(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("?")))
	if !strings.Contains(out, "<Idle|M:0.000,0.000,0.000|") {
		t.Fatalf("status response = %q", out)
	}
	for _, unwanted := range []string{"|MPos:", "|WPos:", "|FS:"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("status response contains %q: %q", unwanted, out)
		}
	}
}

func TestStatusReportPositionFieldVariants(t *testing.T) {
	for _, field := range []string{"MPos", "WPos", "W"} {
		t.Run(field, func(t *testing.T) {
			fw := DefaultFirmwareProfile()
			fw.StatusPositionField = field
			c := NewController(fw, DefaultMachineProfile(), &ManualClock{T: time.Unix(0, 0)})
			out := joined(c.ProcessBytes([]byte("?")))
			want := "<Idle|" + field + ":0.000,0.000,0.000|"
			if !strings.Contains(out, want) || !strings.HasSuffix(out, fw.LineEnding) {
				t.Fatalf("status response = %q, want %q and terminal %q", out, want, fw.LineEnding)
			}
		})
	}
}

func TestStatusReportIncludesWCOAndFS(t *testing.T) {
	fw := DefaultFirmwareProfile()
	fw.StatusPositionField = "M"
	fw.StatusWCOEnabled = true
	fw.StatusWCO = [3]float64{1, 2, -3.5}
	fw.StatusFSEnabled = true
	fw.StatusFS = [2]float64{123, 456}
	c := NewController(fw, DefaultMachineProfile(), &ManualClock{T: time.Unix(0, 0)})
	out := joined(c.ProcessBytes([]byte("?")))
	want := "|M:0.000,0.000,0.000|W:1.000,2.000,-3.500|FS:123,456|B:"
	if !strings.Contains(out, want) {
		t.Fatalf("status response = %q, want segment %q", out, want)
	}
}

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

func TestWCSOffsetsQuery(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("$#\n")))
	if !strings.Contains(out, "[MSG:wcs-dump]\r\n") || !strings.Contains(out, "[G54:0.000,0.000,0.000]\r\n") || !strings.Contains(out, "[G59:0.000,0.000,0.000]\r\n") {
		t.Fatalf("WCS response missing offsets: %q", out)
	}
	if !strings.HasSuffix(out, "ok\r\n") {
		t.Fatalf("WCS response should end in ok: %q", out)
	}
	snap := c.Snapshot()
	if snap.LastCommand != "$#" {
		t.Fatalf("LastCommand = %q, want $#", snap.LastCommand)
	}
	if len(c.commands) == 0 || c.commands[len(c.commands)-1].Text != "$#" {
		t.Fatalf("$# not logged as command: %+v", c.commands)
	}
	if len(c.responses) < 7 || c.responses[len(c.responses)-1].Text != "ok" {
		t.Fatalf("unexpected response log: %+v", c.responses)
	}
}

func TestWCSWriteAccepted(t *testing.T) {
	tests := []struct {
		line       string
		normalized string
	}{
		{"G10 L2 P1 Z-1.250000\n", "G10L2P1Z-1.250000"},
		{"G10L2P2X2.000000\n", "G10L2P2X2.000000"},
		{"G10 L2 P6 Y0.000000\n", "G10L2P6Y0.000000"},
	}

	for _, tt := range tests {
		t.Run(tt.normalized, func(t *testing.T) {
			c, _ := testCtl()
			out := joined(c.ProcessBytes([]byte(tt.line)))
			if !strings.HasSuffix(out, "ok\r\n") {
				t.Fatalf("response = %q, want terminal ok", out)
			}
			if !hasLog(c.commands, "command", tt.normalized) {
				t.Fatalf("normalized command %q not logged: %+v", tt.normalized, c.commands)
			}
			if got := c.Snapshot().State; got != StateIdle {
				t.Fatalf("state = %q, want %q", got, StateIdle)
			}
		})
	}
}

func TestWCSWriteReadback(t *testing.T) {
	c, _ := testCtl()
	if out := joined(c.ProcessBytes([]byte("G10 L2 P1 Z-1.250000\n"))); !strings.HasSuffix(out, "ok\r\n") {
		t.Fatalf("write response = %q, want terminal ok", out)
	}
	out := joined(c.ProcessBytes([]byte("$#\n")))
	for _, want := range []string{
		"[G54:0.000,0.000,-1.250]", "[G55:0.000,0.000,0.000]",
		"[G56:0.000,0.000,0.000]", "[G57:0.000,0.000,0.000]",
		"[G58:0.000,0.000,0.000]", "[G59:0.000,0.000,0.000]",
	} {
		if !strings.Contains(out, want+"\r\n") {
			t.Fatalf("readback missing %q: %q", want, out)
		}
	}
	if !strings.HasSuffix(out, "ok\r\n") {
		t.Fatalf("readback response = %q, want terminal ok", out)
	}
}

func TestWCSWriteReadbackIsIsolated(t *testing.T) {
	c, _ := testCtl()
	for _, command := range []string{"G10 L2 P2 X2.000000\n", "G10 L2 P6 Y-4.500000\n"} {
		if out := joined(c.ProcessBytes([]byte(command))); !strings.HasSuffix(out, "ok\r\n") {
			t.Fatalf("write %q response = %q, want terminal ok", command, out)
		}
	}
	out := joined(c.ProcessBytes([]byte("$#\n")))
	for _, want := range []string{
		"[G54:0.000,0.000,0.000]", "[G55:2.000,0.000,0.000]", "[G59:0.000,-4.500,0.000]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("readback missing %q: %q", want, out)
		}
	}
	if got := c.Snapshot().State; got != StateIdle {
		t.Fatalf("state = %q, want %q", got, StateIdle)
	}
}

func TestWCSWriteRejected(t *testing.T) {
	tests := []struct {
		line       string
		normalized string
	}{
		{"G10 L2 P0 Z1\n", "G10L2P0Z1"},
		{"G10 L2 P7 Z1\n", "G10L2P7Z1"},
		{"G10 L2 P1 A1\n", "G10L2P1A1"},
		{"G10 L2 P1 Zbad\n", "G10L2P1ZBAD"},
		{"G10 L2 P1 X1 Y2\n", "G10L2P1X1Y2"},
		{"G10 L2 P1 XNaN\n", "G10L2P1XNAN"},
		{"G10 L2 P1 X+Inf\n", "G10L2P1X+INF"},
		{"G10 L20 P1 Z1\n", "G10L20P1Z1"},
	}

	for _, tt := range tests {
		t.Run(tt.normalized, func(t *testing.T) {
			c, _ := testCtl()
			out := joined(c.ProcessBytes([]byte(tt.line)))
			if !strings.Contains(out, "[echo: "+tt.normalized+"]\r\n") || !strings.Contains(out, "error:20\r\n") {
				t.Fatalf("response = %q, want echoed terminal error:20", out)
			}
			if got := c.Snapshot().State; got != StateIdle {
				t.Fatalf("state = %q, want %q", got, StateIdle)
			}
		})
	}
}

func TestProbeAcceptedContact(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("G38.2 Z-5 F100\n")))
	if !strings.Contains(out, "[PRB:0.000,0.000,-3.500:1]\r\n") || !strings.HasSuffix(out, "ok\r\n") {
		t.Fatalf("probe response = %q", out)
	}
	if !hasLog(c.commands, "command", "G38.2Z-5F100") {
		t.Fatalf("normalized probe command not logged: %+v", c.commands)
	}
	snap := c.Snapshot()
	if snap.State != StateIdle || snap.MachinePosition != [3]float64{0, 0, -3.5} {
		t.Fatalf("snapshot after probe contact = %+v", snap)
	}
}

func TestProbeAcceptedNoContact(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("G38.2 Z-1 F100\n")))
	if !strings.Contains(out, "[PRB:0.000,0.000,-1.000:0]\r\n") || !strings.HasSuffix(out, "ok\r\n") {
		t.Fatalf("probe response = %q", out)
	}
	if !hasLog(c.commands, "command", "G38.2Z-1F100") {
		t.Fatalf("normalized probe command not logged: %+v", c.commands)
	}
	// A no-contact result reports the commanded endpoint but deliberately does not
	// move the mock machine, which does not model the probe's full travel.
	snap := c.Snapshot()
	if snap.State != StateIdle || snap.MachinePosition != [3]float64{} {
		t.Fatalf("snapshot after no contact = %+v", snap)
	}
}

func TestProbeRejected(t *testing.T) {
	tests := []struct {
		line       string
		normalized string
	}{
		{"G38.2 Z-5\n", "G38.2Z-5"},
		{"G38.2 Z-5 F0\n", "G38.2Z-5F0"},
		{"G38.2 A-5 F100\n", "G38.2A-5F100"},
		{"G38.3 Z-5 F100\n", "G38.3Z-5F100"},
		{"G38.2 Zbad F100\n", "G38.2ZBADF100"},
		{"G38.2 Z-5 Fbad\n", "G38.2Z-5FBAD"},
		{"G38.2 Z-5 F100 X1\n", "G38.2Z-5F100X1"},
		{"G38.2 ZNaN F100\n", "G38.2ZNANF100"},
		{"G38.2 Z-Inf F100\n", "G38.2Z-INFF100"},
	}

	for _, tt := range tests {
		t.Run(tt.normalized, func(t *testing.T) {
			c, _ := testCtl()
			out := joined(c.ProcessBytes([]byte(tt.line)))
			if !strings.Contains(out, "[echo: "+tt.normalized+"]\r\n") || !strings.Contains(out, "error:20\r\n") {
				t.Fatalf("response = %q, want echoed terminal error:20", out)
			}
			if got := c.Snapshot().State; got != StateIdle {
				t.Fatalf("state = %q, want %q", got, StateIdle)
			}
		})
	}
}

func TestSettingsDump(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("$$\n")))
	if !strings.Contains(out, "$0=10\r\n") || !strings.Contains(out, "$132=78.500\r\n") || !strings.HasSuffix(out, "ok\r\n") {
		t.Fatalf("unexpected settings response: %q", out)
	}
	if !hasLog(c.responses, "response", "$0=10") || !hasLog(c.responses, "response", "$132=78.500") || !hasLog(c.responses, "response", "ok") {
		t.Fatalf("settings responses not logged: %+v", c.responses)
	}
	if !hasLog(c.commands, "command", "$$") {
		t.Fatalf("settings command not logged: %+v", c.commands)
	}
	if got := c.Snapshot().State; got != StateIdle {
		t.Fatalf("state = %q, want %q", got, StateIdle)
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

func TestManualHoldResumeState(t *testing.T) {
	c, _ := testCtl()
	c.ProcessBytes([]byte("!"))
	if snap := c.Snapshot(); snap.LastCommand != "!" || snap.State != StateHold {
		t.Fatalf("hold snapshot = %+v", snap)
	}
	if out := joined(c.ProcessBytes([]byte("?"))); !strings.Contains(out, "<Hold|") {
		t.Fatalf("hold status = %q", out)
	}

	c.ProcessBytes([]byte("~"))
	if snap := c.Snapshot(); snap.LastCommand != "~" || snap.State != StateIdle {
		t.Fatalf("resume snapshot = %+v", snap)
	}
	if out := joined(c.ProcessBytes([]byte("?"))); !strings.Contains(out, "<Idle|") {
		t.Fatalf("resume status = %q", out)
	}
}

func TestHomeResetsPositionAndReturnsIdle(t *testing.T) {
	c, clk := testCtl()
	if out := joined(c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))); out != "ok\r\n" {
		t.Fatalf("jog response = %q", out)
	}
	clk.Advance(time.Second)
	if got := c.Snapshot().MachinePosition; got == DefaultMachineProfile().InitialPosition {
		t.Fatalf("position did not change: %v", got)
	}

	if out := joined(c.ProcessBytes([]byte("$H\n"))); out != "ok\r\n" {
		t.Fatalf("home response = %q", out)
	}
	snap := c.Snapshot()
	if !hasLog(c.Commands(), "command", "$H") || snap.State != StateIdle ||
		snap.MachinePosition != DefaultMachineProfile().InitialPosition || snap.ActiveMove != nil || snap.QueuedCommandCount != 0 {
		t.Fatalf("home snapshot = %+v; commands=%+v", snap, c.Commands())
	}
}

func TestSoftResetEmitsResetAlarmStartupAndClearsMotion(t *testing.T) {
	c, _ := testCtl()
	for _, command := range []string{"$J=G53 G90 X-10 F60\n", "$J=G53 G90 Y-10 F60\n"} {
		if out := joined(c.ProcessBytes([]byte(command))); out != "ok\r\n" {
			t.Fatalf("jog response = %q", out)
		}
	}
	if snap := c.Snapshot(); snap.ActiveMove == nil || snap.QueuedCommandCount != 1 {
		t.Fatalf("pre-reset snapshot = %+v", snap)
	}

	assertResetResult(t, c, joined(c.ProcessBytes([]byte{0x18})), "Ctrl-X")
}

func TestAlternateResetMatchesSoftReset(t *testing.T) {
	c, _ := testCtl()
	c.ProcessBytes([]byte("$J=G53 G90 X-10 F60\n"))
	if snap := c.Snapshot(); snap.ActiveMove == nil {
		t.Fatalf("pre-reset snapshot = %+v", snap)
	}

	assertResetResult(t, c, joined(c.ProcessBytes([]byte("|"))), "|")
}

func TestUnlockClearsResetAlarm(t *testing.T) {
	c, _ := testCtl()
	c.ProcessBytes([]byte{0x18})
	if got := c.Snapshot().LastErrorAlarm; got != "ALARM:3" {
		t.Fatalf("reset alarm = %q", got)
	}
	if out := joined(c.ProcessBytes([]byte("$X\n"))); out != "ok\r\n" {
		t.Fatalf("unlock response = %q", out)
	}
	snap := c.Snapshot()
	if snap.State != StateIdle || snap.LastErrorAlarm != "" || !hasLog(c.Commands(), "command", "$X") {
		t.Fatalf("unlock snapshot = %+v; commands=%+v", snap, c.Commands())
	}
}

func assertResetResult(t *testing.T, c *Controller, out, command string) {
	t.Helper()
	want := []string{"[MSG:reset]", "ALARM:3", "Grbl 1.1g [help:'$']"}
	last := -1
	for _, text := range want {
		at := strings.Index(out, text)
		if at <= last {
			t.Fatalf("reset response order = %q", out)
		}
		last = at
	}
	snap := c.Snapshot()
	if snap.State != StateIdle || snap.ActiveMove != nil || snap.QueuedCommandCount != 0 ||
		snap.FreePlannerBlocks != snap.QueueCapacity || snap.LastErrorAlarm != "ALARM:3" ||
		!hasLog(c.Commands(), "command", command) || !hasLog(c.Events(), "realtime", command) {
		t.Fatalf("reset snapshot = %+v; commands=%+v; events=%+v", snap, c.Commands(), c.Events())
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
