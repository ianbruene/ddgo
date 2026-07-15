//go:build linux && serial && mockgrbl_e2e

package mockgrbl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/app"
	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/transport"
)

type mockProcess struct {
	SerialPath string
	HTTPBase   string
	LogPath    string
	client     *http.Client
}

type mockState struct {
	State              string      `json:"state"`
	MachinePosition    [3]float64  `json:"machine_position"`
	ActiveMove         interface{} `json:"active_move"`
	QueuedCommandCount int         `json:"queued_command_count"`
}

func startMockGRBL(t *testing.T) *mockProcess {
	t.Helper()

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "mockgrbl")
	serialPath := filepath.Join(tmp, "mockgrbl-serial")
	logPath := filepath.Join(tmp, "mockgrbl.log")
	httpAddr := freeLocalAddr(t)

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", bin, "./cmd/mockgrbl")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mockgrbl: %v\n%s", err, out)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create mockgrbl log: %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "-symlink", serialPath, "-http", httpAddr)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start mockgrbl: %v", err)
	}

	m := &mockProcess{
		SerialPath: serialPath,
		HTTPBase:   "http://" + httpAddr,
		LogPath:    logPath,
		client:     &http.Client{Timeout: 500 * time.Millisecond},
	}

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			m.dumpDiagnostics(t)
		}
	})

	waitFor(t, 10*time.Second, func() bool {
		if cmd.ProcessState != nil {
			return false
		}
		info, err := os.Lstat(serialPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return false
		}
		_, err = m.fetchState()
		return err == nil
	})

	return m
}

func (m *mockProcess) state(t *testing.T) mockState {
	t.Helper()
	state, err := m.fetchState()
	if err != nil {
		t.Fatalf("fetch mock state: %v", err)
	}
	return state
}

func (m *mockProcess) fetchState() (mockState, error) {
	resp, err := m.client.Get(m.HTTPBase + "/state")
	if err != nil {
		return mockState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mockState{}, fmt.Errorf("GET /state: %s", resp.Status)
	}
	var state mockState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return mockState{}, err
	}
	return state, nil
}

func (m *mockProcess) dumpDiagnostics(t *testing.T) {
	t.Helper()
	if state, err := m.fetchState(); err == nil {
		t.Logf("mock /state: %+v", state)
	} else {
		t.Logf("mock /state unavailable: %v", err)
	}
	if b, err := os.ReadFile(m.LogPath); err == nil {
		t.Logf("mockgrbl log:\n%s", b)
	} else {
		t.Logf("mockgrbl log unavailable: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free local port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close free local port listener: %v", err)
	}
	return addr
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func TestMockGRBLHarnessStarts(t *testing.T) {
	m := startMockGRBL(t)
	state := m.state(t)
	if state.State != "Idle" {
		t.Fatalf("initial state = %q, want Idle", state.State)
	}
	info, err := os.Lstat(m.SerialPath)
	if err != nil {
		t.Fatalf("serial symlink missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("serial path %q is not a symlink", m.SerialPath)
	}
}

func connectControllerToMock(t *testing.T, m *mockProcess) *app.Controller {
	t.Helper()
	controller := app.NewController(transport.NewSerialTransport(), nil)
	eventsCtx, stopEvents := context.WithCancel(context.Background())
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case <-eventsCtx.Done():
				return
			case <-controller.Events():
			}
		}
	}()
	t.Cleanup(func() {
		if err := controller.Disconnect(); err != nil && controller.Snapshot().Connected {
			t.Logf("disconnect controller: %v", err)
		}
		stopEvents()
		<-drainDone
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := controller.Connect(ctx, transport.DefaultPortConfig(m.SerialPath)); err != nil {
		t.Fatalf("connect controller to mock: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return controller.Snapshot().Connected })
	return controller
}

func TestDDGoConnectsToMockAndReadsStatus(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.Action(ctx, grbl.ActionStatus); err != nil {
		t.Fatalf("request status: %v", err)
	}

	var snapshot app.State
	waitFor(t, 5*time.Second, func() bool {
		snapshot = controller.Snapshot()
		return snapshot.Connected && snapshot.MachineState != "" && snapshot.HasMachinePosition
	})
	if snapshot.MachineState != "Idle" {
		t.Fatalf("machine state = %q, want Idle; snapshot=%+v", snapshot.MachineState, snapshot)
	}
	for axis, got := range snapshot.MachinePosition {
		if !near(got, 0, 0.001) {
			t.Fatalf("initial machine position[%d] = %v, want near 0; snapshot=%+v", axis, got, snapshot)
		}
	}
}

func TestDDGoJogToEndpointThenStopAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.JogTo(ctx, "X", -86.5, 60); err != nil {
		t.Fatalf("jog to endpoint: %v", err)
	}

	var beforeMove mockState
	moveDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(moveDeadline) {
		state, err := m.fetchState()
		if err == nil {
			beforeMove = state
			if state.State == "Jog" || state.ActiveMove != nil {
				goto observedMove
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("mock did not report active jog; last state=%+v", beforeMove)

observedMove:

	var moving mockState
	waitFor(t, 5*time.Second, func() bool {
		state, err := m.fetchState()
		if err != nil {
			return false
		}
		moving = state
		x := state.MachinePosition[0]
		return x < -0.01 && x > -86.49
	})

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := controller.StopMotion(stopCtx); err != nil {
		t.Fatalf("stop motion after moving state %+v: %v", moving, err)
	}

	var final mockState
	waitFor(t, 5*time.Second, func() bool {
		state, err := m.fetchState()
		if err != nil {
			return false
		}
		final = state
		return state.State == "Idle" && state.ActiveMove == nil && state.QueuedCommandCount == 0
	})
	if got := final.MachinePosition[0]; got >= 0 || got <= -86.5 {
		t.Fatalf("final X = %v, want stopped between 0 and -86.5; final=%+v", got, final)
	}

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request final status: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		snapshot := controller.Snapshot()
		return snapshot.MachineState == "Idle" && snapshot.HasMachinePosition && near(snapshot.MachinePosition[0], final.MachinePosition[0], 0.25)
	})
}

func near(got, want, tol float64) bool {
	if got < want {
		return want-got <= tol
	}
	return got-want <= tol
}
