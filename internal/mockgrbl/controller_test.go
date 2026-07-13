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
	if !strings.Contains(out, "[MSG:Soft Lim]\r\nALARM:2") {
		t.Fatal(out)
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
func TestMalformedAndHardLimit(t *testing.T) {
	c, _ := testCtl()
	out := joined(c.ProcessBytes([]byte("G2 X1\n")))
	if !strings.Contains(out, "G2X1\r\n[MSG:Unsupported]\r\nerror:20") {
		t.Fatal(out)
	}
	out = joined(c.HardLimit("X"))
	if out != "[MSG:Limit X]\r\nALARM:1\r\n" {
		t.Fatal(out)
	}
}
