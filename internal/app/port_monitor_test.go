package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

func knownMachine(name, serial string) ports.Info {
	// The tested production controller is an Arduino Due native USB device.
	// Linux does not reliably populate SerialNumber, so classification uses its
	// verified 2341:003e VID/PID; serial remains useful only for identity tests.
	return ports.Info{Name: name, IsUSB: true, VID: "2341", PID: "003e", SerialNumber: serial}
}

type countedTransport struct {
	*transport.FakeTransport
	mu    sync.Mutex
	opens []string
}

func newCountedTransport() *countedTransport {
	return &countedTransport{FakeTransport: transport.NewFakeTransport()}
}
func (t *countedTransport) Open(ctx context.Context, cfg transport.PortConfig) (transport.ConnectionGeneration, error) {
	t.mu.Lock()
	t.opens = append(t.opens, cfg.Name)
	t.mu.Unlock()
	return t.FakeTransport.Open(ctx, cfg)
}
func (t *countedTransport) openNames() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.opens...)
}

func TestSelectMachinePort(t *testing.T) {
	m1, m2 := knownMachine("/dev/a", "GrblDD-A"), knownMachine("/dev/b", "GrblDD-B")
	unrelated := ports.Info{Name: "/dev/other", IsUSB: true, VID: "1234", PID: "5678", SerialNumber: "other"}
	tests := []struct {
		name string
		list []ports.Info
		ok   bool
		want string
	}{
		{"empty", nil, false, ""},
		{"unrelated", []ports.Info{unrelated}, false, ""},
		{"machine", []ports.Info{m1}, true, m1.Name},
		{"machine and unrelated", []ports.Info{unrelated, m1}, true, m1.Name},
		{"ambiguous serials", []ports.Info{m1, m2}, false, ""},
		{"missing serial", []ports.Info{{Name: "/dev/a", IsUSB: true, VID: "2341", PID: "003e"}}, true, "/dev/a"},
		{"normalized metadata", []ports.Info{{Name: "/dev/a", IsUSB: true, VID: " 2341 ", PID: "003E"}}, true, "/dev/a"},
		{"non USB", []ports.Info{{Name: "/dev/a", SerialNumber: "GrblDD"}}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := selectMachinePort(tt.list)
			if ok != tt.ok || (ok && got.Name != tt.want) {
				t.Fatalf("selectMachinePort() = (%+v,%v), want name %q, ok %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestPortMonitorImmediateScanAndLaterAppearance(t *testing.T) {
	tr := newCountedTransport()
	var mu sync.Mutex
	list := []ports.Info(nil)
	scanned := make(chan struct{}, 8)
	lister := func(context.Context) ([]ports.Info, error) {
		mu.Lock()
		out := append([]ports.Info(nil), list...)
		mu.Unlock()
		scanned <- struct{}{}
		return out, nil
	}
	c := NewController(tr, lister)
	c.statusPollInterval = time.Hour
	c.portMonitorInterval = 5 * time.Millisecond
	c.StartPortMonitoring(context.Background())
	defer c.StopPortMonitoring()
	select {
	case <-scanned:
	case <-time.After(time.Second):
		t.Fatal("initial enumeration did not happen immediately")
	}
	if len(tr.openNames()) != 0 {
		t.Fatal("opened with no machine")
	}
	mu.Lock()
	list = []ports.Info{knownMachine("/dev/machine", "GrblDD-1")}
	mu.Unlock()
	deadline := time.After(time.Second)
	for !c.Snapshot().Connected {
		select {
		case <-deadline:
			t.Fatal("machine appearance did not connect")
		case <-scanned:
		}
	}
	if got := tr.openNames(); len(got) != 1 || got[0] != "/dev/machine" {
		t.Fatalf("opens = %v", got)
	}
}

func TestPortMonitorRejectsUnrelatedAndAmbiguous(t *testing.T) {
	tr := newCountedTransport()
	list := []ports.Info{{Name: "only", IsUSB: true, SerialNumber: "other"}, knownMachine("a", "GrblDD-A"), knownMachine("b", "GrblDD-B")}
	scanned := make(chan struct{}, 1)
	c := NewController(tr, func(context.Context) ([]ports.Info, error) {
		scanned <- struct{}{}
		return append([]ports.Info(nil), list...), nil
	})
	c.portMonitorInterval = time.Hour
	c.StartPortMonitoring(context.Background())
	<-scanned
	c.StopPortMonitoring()
	if got := tr.openNames(); len(got) != 0 {
		t.Fatalf("unsafe automatic opens = %v", got)
	}
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("a")); err != nil {
		t.Fatalf("manual Connect() = %v", err)
	}
}

func TestAutoConnectBackoffAndTopologyReset(t *testing.T) {
	tr := newCountedTransport()
	tr.SetOpenError(errors.New("not ready"))
	now := time.Unix(100, 0)
	list := []ports.Info{knownMachine("a", "GrblDD-A")}
	c := NewController(tr, func(context.Context) ([]ports.Info, error) { return append([]ports.Info(nil), list...), nil })
	c.now = func() time.Time { return now }
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tr.openNames()) != 1 {
		t.Fatalf("opens during backoff = %v", tr.openNames())
	}
	now = now.Add(time.Second)
	tr.SetOpenError(nil)
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !c.Snapshot().Connected || len(tr.openNames()) != 2 {
		t.Fatalf("retry did not connect; opens=%v state=%+v", tr.openNames(), c.Snapshot())
	}
}

func TestManualDisconnectSuppressedUntilRemoval(t *testing.T) {
	tr := newCountedTransport()
	machine := knownMachine("a", "GrblDD-A")
	current := []ports.Info{machine}
	c := NewController(tr, func(context.Context) ([]ports.Info, error) { return append([]ports.Info(nil), current...), nil })
	c.statusPollInterval = time.Hour
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := c.RefreshPorts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(tr.openNames()) != 1 {
		t.Fatalf("manual suppression opens = %v", tr.openNames())
	}
	current = nil
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	current = []ports.Info{machine}
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tr.openNames()) != 2 || !c.Snapshot().Connected {
		t.Fatalf("replug opens=%v state=%+v", tr.openNames(), c.Snapshot())
	}
}

func TestManualDisconnectSuppressionSurvivesPathChangeAfterRemoval(t *testing.T) {
	tr := newCountedTransport()
	current := []ports.Info{knownMachine("/dev/ttyACM0", "stable-board-1")}
	c := NewController(tr, func(context.Context) ([]ports.Info, error) { return clonePorts(current), nil })
	c.statusPollInterval = time.Hour
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tr.openNames()) != 1 {
		t.Fatalf("reconnected while suppressed: %v", tr.openNames())
	}
	current = nil
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	current = []ports.Info{knownMachine("/dev/ttyACM1", "stable-board-1")}
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tr.openNames(); len(got) != 2 || got[1] != "/dev/ttyACM1" {
		t.Fatalf("path-change opens = %v", got)
	}
}

func TestAutomaticPermissionErrorIsActionableDeduplicatedAndRetried(t *testing.T) {
	tr := newCountedTransport()
	tr.SetOpenError(&os.PathError{Op: "open", Path: "/dev/ttyACM0", Err: os.ErrPermission})
	now := time.Unix(100, 0)
	c := NewController(tr, ports.StaticList([]ports.Info{knownMachine("/dev/ttyACM0", "")}, nil))
	c.now = func() time.Time { return now }
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().Connected {
		t.Fatal("permission-denied open connected")
	}
	if got := countEventsContaining(c.Events(), "does not have permission"); got != 1 {
		t.Fatalf("permission events = %d", got)
	}
	now = now.Add(time.Second)
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tr.openNames()) != 2 {
		t.Fatalf("permission failure was not retried: %v", tr.openNames())
	}
	if got := countEventsContaining(c.Events(), "does not have permission"); got != 0 {
		t.Fatalf("duplicate permission events = %d", got)
	}
	now = now.Add(2 * time.Second)
	tr.SetOpenError(nil)
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !c.Snapshot().Connected {
		t.Fatal("did not connect after permissions became available")
	}
}

func TestManualConnectReportsPermissionFailureImmediately(t *testing.T) {
	tr := newCountedTransport()
	tr.SetOpenError(os.ErrPermission)
	c := NewController(tr, ports.StaticList(nil, nil))
	err := c.Connect(context.Background(), transport.DefaultPortConfig("/dev/ttyACM0"))
	if err == nil || !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "dialout-group membership") {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := countEventsContaining(c.Events(), "does not have permission"); got != 1 {
		t.Fatalf("manual permission events = %d", got)
	}
}

func countEventsContaining(events <-chan Event, text string) int {
	n := 0
	for {
		select {
		case ev := <-events:
			if strings.Contains(ev.Text, text) {
				n++
			}
		default:
			return n
		}
	}
}

func TestUnexpectedDisconnectDoesNotSuppressAutoConnect(t *testing.T) {
	tr := newCountedTransport()
	machine := knownMachine("a", "GrblDD-A")
	c := NewController(tr, ports.StaticList([]ports.Info{machine}, nil))
	c.statusPollInterval = time.Hour
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr.InjectDisconnected()
	waitForState(t, c, func(s State) bool { return !s.Connected })
	if err := c.RefreshPorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tr.openNames()) != 2 {
		t.Fatalf("opens after transport loss = %v", tr.openNames())
	}
}

func TestStopPortMonitorStopsScanning(t *testing.T) {
	var mu sync.Mutex
	scans := 0
	scanned := make(chan struct{}, 4)
	c := NewController(newCountedTransport(), func(context.Context) ([]ports.Info, error) {
		mu.Lock()
		scans++
		mu.Unlock()
		scanned <- struct{}{}
		return nil, nil
	})
	c.portMonitorInterval = time.Hour
	c.StartPortMonitoring(context.Background())
	<-scanned
	c.StopPortMonitoring()
	mu.Lock()
	defer mu.Unlock()
	if scans != 1 {
		t.Fatalf("scans before stop completed = %d, want 1", scans)
	}
}
