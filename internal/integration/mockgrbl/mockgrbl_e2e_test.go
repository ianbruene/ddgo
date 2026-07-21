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
	"strings"
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

type mockLogEntry struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

const posTol = 0.05

const resetPosTol = 0.25

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

func (m *mockProcess) responses(t *testing.T) []mockLogEntry {
	t.Helper()
	var responses []mockLogEntry
	m.getJSON(t, "/responses", &responses)
	return responses
}

func (m *mockProcess) events(t *testing.T) []mockLogEntry {
	t.Helper()
	var events []mockLogEntry
	m.getJSON(t, "/events", &events)
	return events
}

func (m *mockProcess) fetchState() (mockState, error) {
	var state mockState
	if err := m.fetchJSON("/state", &state); err != nil {
		return mockState{}, err
	}
	return state, nil
}

func (m *mockProcess) getJSON(t *testing.T, path string, out any) {
	t.Helper()
	if err := m.fetchJSON(path, out); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

func (m *mockProcess) fetchJSON(path string, out any) error {
	resp, err := m.client.Get(m.HTTPBase + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m *mockProcess) dumpDiagnostics(t *testing.T) {
	t.Helper()
	if state, err := m.fetchState(); err == nil {
		t.Logf("mock /state: %+v", state)
	} else {
		t.Logf("mock /state unavailable: %v", err)
	}
	var responses []mockLogEntry
	if err := m.fetchJSON("/responses", &responses); err == nil {
		t.Logf("mock /responses: %+v", responses)
	} else {
		t.Logf("mock /responses unavailable: %v", err)
	}
	var events []mockLogEntry
	if err := m.fetchJSON("/events", &events); err == nil {
		t.Logf("mock /events: %+v", events)
	} else {
		t.Logf("mock /events unavailable: %v", err)
	}
	if b, err := os.ReadFile(m.LogPath); err == nil {
		t.Logf("mockgrbl log %s:\n%s", m.LogPath, b)
	} else {
		t.Logf("mockgrbl log %s unavailable: %v", m.LogPath, err)
	}
}

func waitForMockState(t *testing.T, m *mockProcess, timeout time.Duration, pred func(mockState) bool) mockState {
	t.Helper()
	var last mockState
	var lastErr error
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := m.fetchState()
		if err == nil {
			last = state
			if pred(state) {
				return state
			}
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("mock state condition not met within %s; last=%+v; lastErr=%v", timeout, last, lastErr)
	return last
}

func waitForMockResponses(t *testing.T, m *mockProcess, timeout time.Duration, pred func([]mockLogEntry) bool) []mockLogEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []mockLogEntry
	var lastErr error
	for time.Now().Before(deadline) {
		var responses []mockLogEntry
		err := m.fetchJSON("/responses", &responses)
		if err == nil {
			last = responses
			if pred(responses) {
				return responses
			}
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	var events []mockLogEntry
	if err := m.fetchJSON("/events", &events); err != nil {
		t.Fatalf("mock response condition not met within %s; last=%+v; lastErr=%v; eventsErr=%v", timeout, last, lastErr, err)
	}
	t.Fatalf("mock response condition not met within %s; last=%+v; lastErr=%v; events=%+v", timeout, last, lastErr, events)
	return last
}

func waitForMockEvents(t *testing.T, m *mockProcess, timeout time.Duration, pred func([]mockLogEntry) bool) []mockLogEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []mockLogEntry
	var lastErr error
	for time.Now().Before(deadline) {
		var events []mockLogEntry
		err := m.fetchJSON("/events", &events)
		if err == nil {
			last = events
			if pred(events) {
				return events
			}
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("mock event condition not met within %s; last=%+v; lastErr=%v", timeout, last, lastErr)
	return last
}

func assertMockStateRemains(t *testing.T, m *mockProcess, duration time.Duration, pred func(mockState) bool) {
	t.Helper()
	deadline := time.Now().Add(duration)
	var last mockState
	var lastErr error
	for time.Now().Before(deadline) {
		state, err := m.fetchState()
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		last = state
		if !pred(state) {
			t.Fatalf("mock state changed during %s; state=%+v", duration, state)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if lastErr != nil && last.State == "" {
		t.Fatalf("mock state unavailable during %s; lastErr=%v", duration, lastErr)
	}
}

func waitForControllerState(t *testing.T, c *app.Controller, timeout time.Duration, pred func(app.State) bool) app.State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last app.State
	for time.Now().Before(deadline) {
		last = c.Snapshot()
		if pred(last) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("controller state condition not met within %s; last=%+v", timeout, last)
	return last
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

func TestDDGoJogToEndpointCompletesAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request baseline status: %v", err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.HasMachinePosition
	})

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", -1.0, 60); err != nil {
		t.Fatalf("jog to endpoint: %v", err)
	}

	finalMock := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" &&
			state.ActiveMove == nil &&
			state.QueuedCommandCount == 0 &&
			near(state.MachinePosition[0], -1.0, posTol)
	})

	statusCtx, statusCancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request final status after mock state %+v: %v", finalMock, err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.HasMachinePosition &&
			near(snapshot.MachinePosition[0], -1.0, posTol) &&
			snapshot.MachineState == "Idle"
	})
}

func TestDDGoQueuedAbsoluteJogSequencingAgainstMock(t *testing.T) {
	const queuedJogTarget = -3.0
	const queuedJogFeed = 120.0

	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request baseline status: %v", err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle"
	})

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", queuedJogTarget, queuedJogFeed); err != nil {
		t.Fatalf("queue X absolute jog: %v", err)
	}
	if err := controller.JogTo(jogCtx, "Y", queuedJogTarget, queuedJogFeed); err != nil {
		t.Fatalf("queue Y absolute jog: %v", err)
	}
	if err := controller.JogTo(jogCtx, "Z", queuedJogTarget, queuedJogFeed); err != nil {
		t.Fatalf("queue Z absolute jog: %v", err)
	}

	queued := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Jog" &&
			state.ActiveMove != nil &&
			state.QueuedCommandCount > 0
	})
	t.Logf("observed queued jog state: %+v", queued)

	finalMock := waitForMockState(t, m, 15*time.Second, func(state mockState) bool {
		return state.State == "Idle" &&
			state.ActiveMove == nil &&
			state.QueuedCommandCount == 0 &&
			near(state.MachinePosition[0], queuedJogTarget, posTol) &&
			near(state.MachinePosition[1], queuedJogTarget, posTol) &&
			near(state.MachinePosition[2], queuedJogTarget, posTol)
	})

	statusCtx, statusCancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request final status after mock state %+v: %v", finalMock, err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], queuedJogTarget, posTol) &&
			near(snapshot.MachinePosition[1], queuedJogTarget, posTol) &&
			near(snapshot.MachinePosition[2], queuedJogTarget, posTol)
	})
}

func TestDDGoRealtimeHoldResumeDuringJogAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request baseline status: %v", err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], 0, posTol)
	})

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", -86.5, 60); err != nil {
		t.Fatalf("start long absolute jog: %v", err)
	}

	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Jog" || state.ActiveMove != nil
	})
	moving := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		x := state.MachinePosition[0]
		return x < -0.01 && x > -86.49
	})

	holdCtx, holdCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer holdCancel()
	if err := controller.Action(holdCtx, grbl.ActionHold); err != nil {
		t.Fatalf("feed hold during moving state %+v: %v", moving, err)
	}

	finalMock := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" &&
			state.ActiveMove == nil &&
			state.QueuedCommandCount == 0
	})
	heldX := finalMock.MachinePosition[0]
	if heldX >= 0 || heldX <= -86.5 {
		t.Fatalf("held X = %v, want materialized position between 0 and -86.5; final=%+v", heldX, finalMock)
	}
	if events := m.events(t); !hasMockLogEntry(events, "command", "!") {
		t.Fatalf("missing realtime hold command event; events=%+v", events)
	}

	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer resumeCancel()
	if err := controller.Action(resumeCtx, grbl.ActionResume); err != nil {
		t.Fatalf("resume after mock-cancelled jog: %v", err)
	}
	waitForMockEvents(t, m, 2*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "~")
	})
	assertMockStateRemains(t, m, time.Second, func(state mockState) bool {
		return state.State == "Idle" &&
			state.ActiveMove == nil &&
			state.QueuedCommandCount == 0 &&
			near(state.MachinePosition[0], heldX, posTol)
	})

	statusCtx, statusCancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request final status after hold/resume state %+v: %v", finalMock, err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], heldX, resetPosTol)
	})
}

func TestDDGoRealtimeResetDuringJogAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request baseline status: %v", err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], 0, posTol)
	})

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", -86.5, 60); err != nil {
		t.Fatalf("start long absolute jog: %v", err)
	}

	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Jog" || state.ActiveMove != nil
	})
	moving := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		x := state.MachinePosition[0]
		return x < -0.01 && x > -86.49
	})

	resetCtx, resetCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer resetCancel()
	if err := controller.Action(resetCtx, grbl.ActionSoftReset); err != nil {
		t.Fatalf("soft reset during moving state %+v: %v", moving, err)
	}

	finalMock := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" &&
			state.ActiveMove == nil &&
			state.QueuedCommandCount == 0
	})
	if got := finalMock.MachinePosition[0]; got >= 0 || got <= -86.5 {
		t.Fatalf("post-reset X = %v, want materialized position between 0 and -86.5; final=%+v", got, finalMock)
	}

	statusCtx, statusCancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request final status after reset state %+v: %v", finalMock, err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], finalMock.MachinePosition[0], resetPosTol) &&
			near(snapshot.MachinePosition[1], 0, posTol) &&
			near(snapshot.MachinePosition[2], 0, posTol)
	})
}

func TestDDGoStatusReportsDuringAndAfterMockJog(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request baseline status: %v", err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.MachineState == "Idle" &&
			snapshot.HasMachinePosition &&
			near(snapshot.MachinePosition[0], 0, posTol)
	})

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer jogCancel()
	const statusJogTarget = -3.0
	const statusJogFeed = 60.0

	if err := controller.JogTo(jogCtx, "X", statusJogTarget, statusJogFeed); err != nil {
		t.Fatalf("start status-report jog: %v", err)
	}

	movingMock := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		x := state.MachinePosition[0]
		return state.State == "Jog" &&
			state.ActiveMove != nil &&
			x < -0.25 &&
			x > -2.0
	})

	statusCtx, statusCancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request moving status after mock state %+v: %v", movingMock, err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		x := snapshot.MachinePosition[0]
		return snapshot.HasMachinePosition &&
			snapshot.MachineState == "Jog" &&
			x < -0.05 &&
			x > -2.95 &&
			snapshot.LastStatusRaw != ""
	})

	finalMock := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" &&
			state.ActiveMove == nil &&
			state.QueuedCommandCount == 0 &&
			near(state.MachinePosition[0], statusJogTarget, posTol)
	})

	statusCtx, statusCancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request final status after mock state %+v: %v", finalMock, err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.MachineState == "Idle" &&
			snapshot.HasMachinePosition &&
			near(snapshot.MachinePosition[0], statusJogTarget, posTol) &&
			snapshot.LastStatusRaw != ""
	})
}

func TestDDGoJogLimitRejectionAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request baseline status: %v", err)
	}
	baseline := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.HasMachinePosition
	})
	baselineX := baseline.MachinePosition[0]

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", -999.0, 60); err != nil {
		t.Logf("out-of-bounds JogTo returned write error: %v", err)
	}

	responses := waitForMockResponses(t, m, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[MSG:jogLIM]") &&
			hasMockResponse(responses, "error:15")
	})

	finalMock := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" &&
			state.ActiveMove == nil &&
			state.QueuedCommandCount == 0 &&
			near(state.MachinePosition[0], baselineX, posTol)
	})
	if finalMock.State == "Alarm" {
		t.Fatalf("mock entered Alarm after rejected jog: %+v", finalMock)
	}
	if !hasMockResponse(responses, "[MSG:jogLIM]") || !hasMockResponse(responses, "error:15") {
		t.Fatalf("missing jog limit response; responses=%+v; events=%+v", responses, m.events(t))
	}

	statusCtx, statusCancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer statusCancel()
	if err := controller.Action(statusCtx, grbl.ActionStatus); err != nil {
		t.Fatalf("request final status after rejected jog: %v", err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], baselineX, posTol)
	})
}

func hasMockLogEntry(entries []mockLogEntry, kindContains, textContains string) bool {
	for _, entry := range entries {
		if strings.Contains(entry.Kind, kindContains) && strings.Contains(entry.Text, textContains) {
			return true
		}
	}
	return false
}

func hasMockResponse(entries []mockLogEntry, text string) bool {
	for _, entry := range entries {
		if entry.Kind == "response" && strings.Contains(entry.Text, text) {
			return true
		}
	}
	return false
}

func near(got, want, tol float64) bool {
	if got < want {
		return want-got <= tol
	}
	return got-want <= tol
}
