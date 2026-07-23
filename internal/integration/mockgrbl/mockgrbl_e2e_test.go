//go:build linux && serial && mockgrbl_e2e

package mockgrbl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

type controllerHarness struct {
	Controller *app.Controller

	mu     sync.Mutex
	events []app.Event

	stopEvents context.CancelFunc
	drainDone  chan struct{}
}

const posTol = 0.05

const resetPosTol = 0.25

type mockGRBLOptions struct {
	ResponseDelay time.Duration
}

func startMockGRBL(t *testing.T) *mockProcess {
	t.Helper()
	return startMockGRBLWithOptions(t, mockGRBLOptions{})
}

func startMockGRBLWithOptions(t *testing.T, opts mockGRBLOptions) *mockProcess {
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
	args := []string{"-symlink", serialPath, "-http", httpAddr}
	if opts.ResponseDelay > 0 {
		args = append(args, "-response-delay", opts.ResponseDelay.String())
	}
	cmd := exec.CommandContext(ctx, bin, args...)
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

func mockResponseCount(t *testing.T, m *mockProcess) int {
	t.Helper()
	return len(m.responses(t))
}

func mockEventCount(t *testing.T, m *mockProcess) int {
	t.Helper()
	return len(m.events(t))
}

func waitForNewMockResponses(t *testing.T, m *mockProcess, after int, timeout time.Duration, pred func([]mockLogEntry) bool) []mockLogEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastAll []mockLogEntry
	var lastNew []mockLogEntry
	var lastErr error
	for time.Now().Before(deadline) {
		var responses []mockLogEntry
		err := m.fetchJSON("/responses", &responses)
		if err == nil {
			lastAll = responses
			if after <= len(responses) {
				lastNew = responses[after:]
			} else {
				lastNew = nil
			}
			if pred(lastNew) {
				return lastNew
			}
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("new mock response condition not met within %s; after=%d; lastNew=%+v; lastAll=%+v; lastErr=%v", timeout, after, lastNew, lastAll, lastErr)
	return lastNew
}

func waitForNewMockEvents(t *testing.T, m *mockProcess, after int, timeout time.Duration, pred func([]mockLogEntry) bool) []mockLogEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastAll []mockLogEntry
	var lastNew []mockLogEntry
	var lastErr error
	for time.Now().Before(deadline) {
		var events []mockLogEntry
		err := m.fetchJSON("/events", &events)
		if err == nil {
			lastAll = events
			if after <= len(events) {
				lastNew = events[after:]
			} else {
				lastNew = nil
			}
			if pred(lastNew) {
				return lastNew
			}
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("new mock event condition not met within %s; after=%d; lastNew=%+v; lastAll=%+v; lastErr=%v", timeout, after, lastNew, lastAll, lastErr)
	return lastNew
}

func requestStatus(t *testing.T, c *app.Controller) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Action(ctx, grbl.ActionStatus); err != nil {
		t.Fatalf("request status: %v", err)
	}
}

func waitForProgramStatus(t *testing.T, c *app.Controller, timeout time.Duration, status app.ProgramStatus) app.State {
	t.Helper()
	return waitForControllerState(t, c, timeout, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == status
	})
}

func waitForActiveProgramProgress(t *testing.T, c *app.Controller, timeout time.Duration) app.State {
	t.Helper()
	return waitForControllerState(t, c, timeout, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramRunning &&
			snapshot.ProgramComplete > 0 &&
			snapshot.ProgramComplete < snapshot.ProgramTotal
	})
}

func assertControllerStateRemains(t *testing.T, c *app.Controller, duration time.Duration, pred func(app.State) bool) {
	t.Helper()
	deadline := time.Now().Add(duration)
	var last app.State
	for time.Now().Before(deadline) {
		last = c.Snapshot()
		if !pred(last) {
			t.Fatalf("controller state changed during %s; state=%+v", duration, last)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func requireControllerIdle(t *testing.T, c *app.Controller) app.State {
	t.Helper()
	return waitForControllerState(t, c, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle"
	})
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

func assertNoNewMockCommandContainingFor(t *testing.T, m *mockProcess, after int, duration time.Duration, forbidden ...string) {
	t.Helper()

	deadline := time.Now().Add(duration)
	var lastNew []mockLogEntry
	var lastErr error

	for time.Now().Before(deadline) {
		var events []mockLogEntry
		err := m.fetchJSON("/events", &events)
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}

		if after <= len(events) {
			lastNew = events[after:]
		} else {
			lastNew = nil
		}

		for _, entry := range lastNew {
			if entry.Kind != "command" {
				continue
			}
			for _, text := range forbidden {
				if strings.Contains(entry.Text, text) {
					t.Fatalf("forbidden mock command %q observed during %s; events=%+v", text, duration, lastNew)
				}
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	if lastErr != nil && lastNew == nil {
		t.Fatalf("mock events unavailable while checking forbidden commands during %s; lastErr=%v", duration, lastErr)
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
			case _, ok := <-controller.Events():
				if !ok {
					return
				}
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

func connectControllerToMockWithEvents(t *testing.T, m *mockProcess) *controllerHarness {
	t.Helper()

	h := &controllerHarness{
		Controller: app.NewController(transport.NewSerialTransport(), nil),
		drainDone:  make(chan struct{}),
	}

	eventsCtx, stopEvents := context.WithCancel(context.Background())
	h.stopEvents = stopEvents

	go func() {
		defer close(h.drainDone)
		for {
			select {
			case <-eventsCtx.Done():
				return
			case event, ok := <-h.Controller.Events():
				if !ok {
					return
				}
				h.mu.Lock()
				h.events = append(h.events, event)
				h.mu.Unlock()
			}
		}
	}()

	t.Cleanup(func() {
		if err := h.Controller.Disconnect(); err != nil && h.Controller.Snapshot().Connected {
			t.Logf("disconnect controller: %v", err)
		}
		h.stopEvents()
		<-h.drainDone
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Controller.Connect(ctx, transport.DefaultPortConfig(m.SerialPath)); err != nil {
		t.Fatalf("connect controller to mock: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return h.Controller.Snapshot().Connected })
	return h
}

func (h *controllerHarness) eventCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (h *controllerHarness) eventsAfter(after int) []app.Event {
	h.mu.Lock()
	defer h.mu.Unlock()

	if after > len(h.events) {
		return nil
	}
	out := make([]app.Event, len(h.events[after:]))
	copy(out, h.events[after:])
	return out
}

func (h *controllerHarness) waitForEventsAfter(t *testing.T, after int, timeout time.Duration, pred func([]app.Event) bool) []app.Event {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var last []app.Event
	for time.Now().Before(deadline) {
		last = h.eventsAfter(after)
		if pred(last) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("controller event condition not met within %s; after=%d; last=%+v", timeout, after, last)
	return last
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

func TestDDGoConsoleBuildInfoAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.SendConsoleLine(ctx, "$I"); err != nil {
		t.Fatalf("send build-info console line: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") &&
			hasMockResponse(responses, "GG:") &&
			hasMockResponse(responses, "PCB:") &&
			hasMockResponse(responses, "YMD:") &&
			hasMockResponse(responses, "ok")
	})
	waitForMockEvents(t, m, 2*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$I")
	})

	if snapshot := controller.Snapshot(); !snapshot.Connected || snapshot.LastError != "" {
		t.Fatalf("controller not healthy after build-info response: %+v", snapshot)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoUnlockAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.Action(ctx, grbl.ActionUnlock); err != nil {
		t.Fatalf("unlock controller: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ok")
	})
	waitForNewMockEvents(t, m, eventsAfter, 2*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$X")
	})

	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoUnsupportedConsoleCommandAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	requestStatus(t, controller)
	baseline := requireControllerIdle(t, controller)
	baselineX := baseline.MachinePosition[0]
	responsesAfter := mockResponseCount(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.SendConsoleLine(ctx, "G4 P0.1"); err != nil {
		t.Fatalf("send unsupported console line: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "error:")
	})

	state := m.state(t)
	if state.State != "Idle" || state.ActiveMove != nil || state.QueuedCommandCount != 0 || !near(state.MachinePosition[0], baselineX, posTol) {
		t.Fatalf("mock unsafe after unsupported command; baselineX=%v; state=%+v", baselineX, state)
	}

	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], baselineX, posTol)
	})
}

func TestDDGoConsoleResponsesAreNotConfusedByStatusPollingAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)

	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.HasMachinePosition &&
			snapshot.LastStatusRaw != ""
	})

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.SendConsoleLine(ctx, "$I"); err != nil {
		t.Fatalf("send build-info console line while polling: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") &&
			hasMockResponse(responses, "GG:") &&
			hasMockResponse(responses, "PCB:") &&
			hasMockResponse(responses, "YMD:") &&
			hasMockResponse(responses, "ok")
	})
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$I") &&
			hasMockLogEntry(events, "command", "?")
	})

	if snapshot := controller.Snapshot(); !snapshot.Connected || !snapshot.HasMachinePosition || snapshot.MachineState == "" {
		t.Fatalf("controller missing parsed status after console response during polling: %+v", snapshot)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoConsoleResponseEventsDuringStatusPollingAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.HasMachinePosition &&
			snapshot.LastStatusRaw != ""
	})

	controllerEventsAfter := h.eventCount()
	mockResponsesAfter := mockResponseCount(t, m)
	mockEventsAfter := mockEventCount(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.SendConsoleLine(ctx, "$I"); err != nil {
		t.Fatalf("send build-info console line while polling: %v", err)
	}

	waitForNewMockResponses(t, m, mockResponsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") &&
			hasMockResponse(responses, "GG:") &&
			hasMockResponse(responses, "PCB:") &&
			hasMockResponse(responses, "YMD:") &&
			hasMockResponse(responses, "ok")
	})
	waitForNewMockEvents(t, m, mockEventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$I") &&
			hasMockLogEntry(events, "command", "?")
	})
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "[grbl:") &&
			hasControllerEventText(events, "GG:") &&
			hasControllerEventText(events, "PCB:") &&
			hasControllerEventText(events, "YMD:") &&
			hasControllerEventText(events, "ok")
	})

	if snapshot := controller.Snapshot(); !snapshot.Connected || !snapshot.HasMachinePosition || snapshot.MachineState == "" {
		t.Fatalf("controller missing parsed status after console response events during polling: %+v", snapshot)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoManualControlsBlockedWhileProgramRunningAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeRepeatedGStateProgram(t, "active-blocking-program.gcode", 25)
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load active blocking program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start active blocking program: %v", err)
	}
	actionCtx, actionCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer actionCancel()

	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramRunning &&
			snapshot.ProgramTotal == 25 &&
			snapshot.ProgramComplete < snapshot.ProgramTotal
	})

	if err := controller.JogTo(actionCtx, "X", -1, 120); !errors.Is(err, app.ErrProgramActive) {
		t.Fatalf("JogTo while active error = %v, want %v", err, app.ErrProgramActive)
	}
	if err := controller.Action(actionCtx, grbl.ActionStatus); !errors.Is(err, app.ErrProgramActive) {
		t.Fatalf("Action(status) while active error = %v, want %v", err, app.ErrProgramActive)
	}
	if err := controller.SendConsoleLine(actionCtx, "$I"); !errors.Is(err, app.ErrProgramActive) {
		t.Fatalf("SendConsoleLine while active error = %v, want %v", err, app.ErrProgramActive)
	}

	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return countControllerEventText(events, app.ErrProgramActive.Error()) >= 3
	})
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "$J=", "$I")

	waitForControllerState(t, controller, 10*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramCompleted && snapshot.ProgramComplete == snapshot.ProgramTotal
	})
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoStartProgramWithoutLoadedProgramAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := controller.StartProgram(ctx)
	if err == nil || !strings.Contains(err.Error(), "load a program before starting") {
		t.Fatalf("StartProgram() error = %v, want containing %q", err, "load a program before starting")
	}
	requireProgramErrorEvent(t, h, controllerEventsAfter, "load a program before starting")

	snapshot := controller.Snapshot()
	if snapshot.ProgramStatus != app.ProgramNotLoaded || !snapshot.Connected || snapshot.MachineState != "Idle" {
		t.Fatalf("controller state after rejected start = %+v, want connected idle with no loaded program", snapshot)
	}
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "$G", "$J=", "G4")
}

func TestDDGoStartProgramWhileRunningAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeRepeatedGStateProgram(t, "double-start-running-program.gcode", 25)
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load double-start program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start double-start program: %v", err)
	}
	waitForActiveProgramProgress(t, controller, 5*time.Second)

	progressBefore := controller.Snapshot().ProgramComplete
	controllerEventsAfter := h.eventCount()

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer secondCancel()
	err := controller.StartProgram(secondCtx)
	if err == nil || !strings.Contains(err.Error(), "program is already running") {
		t.Fatalf("second StartProgram() error = %v, want containing %q", err, "program is already running")
	}
	requireProgramErrorEvent(t, h, controllerEventsAfter, "program is already running")

	waitForControllerState(t, controller, 300*time.Millisecond, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramRunning &&
			snapshot.ProgramComplete >= progressBefore &&
			snapshot.ProgramComplete < snapshot.ProgramTotal
	})

	waitForProgramStatus(t, controller, 10*time.Second, app.ProgramCompleted)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoLoadProgramWhileRunningAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	initialPath := writeRepeatedGStateProgram(t, "initial-running-program.gcode", 25)
	replacementPath := writeRepeatedGStateProgram(t, "replacement-program.gcode", 1)
	if err := controller.LoadProgramFile(initialPath); err != nil {
		t.Fatalf("load initial program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start initial program: %v", err)
	}
	waitForActiveProgramProgress(t, controller, 5*time.Second)

	before := controller.Snapshot()
	controllerEventsAfter := h.eventCount()

	err := controller.LoadProgramFile(replacementPath)
	if !errors.Is(err, app.ErrProgramActive) {
		t.Fatalf("LoadProgramFile() while running error = %v, want %v", err, app.ErrProgramActive)
	}
	requireProgramErrorEvent(t, h, controllerEventsAfter, app.ErrProgramActive.Error())

	snapshot := controller.Snapshot()
	if snapshot.ProgramName != "initial-running-program.gcode" ||
		snapshot.ProgramPath != initialPath ||
		snapshot.ProgramTotal != 25 ||
		snapshot.ProgramComplete < before.ProgramComplete ||
		!programStatusIsAny(snapshot.ProgramStatus, app.ProgramRunning, app.ProgramCompleted) {
		t.Fatalf("controller state after rejected load = %+v, before=%+v", snapshot, before)
	}

	waitForProgramStatus(t, controller, 10*time.Second, app.ProgramCompleted)
	final := controller.Snapshot()
	if final.ProgramName != "initial-running-program.gcode" || final.ProgramPath != initialPath || final.ProgramTotal != 25 {
		t.Fatalf("final program metadata = %+v, want initial program", final)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoDisconnectWhileProgramRunningAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeRepeatedGStateProgram(t, "disconnect-running-program.gcode", 25)
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load disconnect-running program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start disconnect-running program: %v", err)
	}
	waitForActiveProgramProgress(t, controller, 5*time.Second)

	before := controller.Snapshot()
	controllerEventsAfter := h.eventCount()

	err := controller.Disconnect()
	if !errors.Is(err, app.ErrProgramActive) {
		t.Fatalf("Disconnect() while running error = %v, want %v", err, app.ErrProgramActive)
	}
	requireProgramErrorEvent(t, h, controllerEventsAfter, app.ErrProgramActive.Error())

	snapshot := controller.Snapshot()
	if !snapshot.Connected ||
		snapshot.PortName == "" ||
		snapshot.ProgramComplete < before.ProgramComplete ||
		!programStatusIsAny(snapshot.ProgramStatus, app.ProgramRunning, app.ProgramCompleted) {
		t.Fatalf("controller state after rejected disconnect = %+v, before=%+v", snapshot, before)
	}

	waitForProgramStatus(t, controller, 10*time.Second, app.ProgramCompleted)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoProgramFailureThenSuccessfulRunAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	failingPath := writeIntegrationProgramFile(t, "failing-program.gcode", "G4 P0.1\n")
	if err := controller.LoadProgramFile(failingPath); err != nil {
		t.Fatalf("load failing program: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start failing program: %v", err)
	}

	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed &&
			strings.Contains(snapshot.LastError, "program failed at line 1: error:")
	})
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	successPath := writeIntegrationProgramFile(t, "success-after-failure.gcode", "$G\n")
	if err := controller.LoadProgramFile(successPath); err != nil {
		t.Fatalf("load success-after-failure program: %v", err)
	}
	requireLoadedProgram(t, controller, "success-after-failure.gcode", 1)

	responsesAfter := mockResponseCount(t, m)
	controllerEventsAfter := h.eventCount()

	successCtx, successCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer successCancel()
	if err := controller.StartProgram(successCtx); err != nil {
		t.Fatalf("start success-after-failure program: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[GC:") &&
			hasMockResponse(responses, "ok")
	})
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "program success-after-failure.gcode completed")
	})
	requireProgramCompleted(t, controller, 1)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoStopProgramDuringActiveMockProgram(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeRepeatedGStateProgram(t, "stop-active-mock-program.gcode", 50)
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load stop active program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	_ = mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start stop active program: %v", err)
	}
	actionCtx, actionCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer actionCancel()

	waitForActiveProgramProgress(t, controller, 5*time.Second)

	if err := controller.StopProgram(actionCtx); err != nil {
		t.Fatalf("stop active program: %v", err)
	}
	waitForProgramStatus(t, controller, 5*time.Second, app.ProgramStopped)
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "program stopped")
	})
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "!")
	})
	state := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" && state.ActiveMove == nil && state.QueuedCommandCount == 0
	})
	if state.State != "Idle" || state.ActiveMove != nil || state.QueuedCommandCount != 0 {
		t.Fatalf("mock unsafe after stop: %+v", state)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoPauseResumeActiveMockProgram(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeRepeatedGStateProgram(t, "pause-resume-active-mock-program.gcode", 50)
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load pause/resume program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	eventsAfter := mockEventCount(t, m)

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start pause/resume program: %v", err)
	}
	actionCtx, actionCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer actionCancel()
	waitForActiveProgramProgress(t, controller, 5*time.Second)

	if err := controller.PauseProgram(actionCtx); err != nil {
		t.Fatalf("pause active program: %v", err)
	}
	waitForProgramStatus(t, controller, 5*time.Second, app.ProgramPaused)
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "!")
	})

	pausedComplete := controller.Snapshot().ProgramComplete
	// One line may already be in flight when PauseProgram sends feed hold. Allow
	// that acknowledgement to land, but require the sender to stop advancing after it.
	assertControllerStateRemains(t, controller, 300*time.Millisecond, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramPaused &&
			snapshot.ProgramComplete >= pausedComplete &&
			snapshot.ProgramComplete <= pausedComplete+1
	})

	if err := controller.ResumeProgram(actionCtx); err != nil {
		t.Fatalf("resume paused program: %v", err)
	}
	waitForProgramStatus(t, controller, 5*time.Second, app.ProgramRunning)
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "~")
	})
	waitForControllerState(t, controller, 10*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramCompleted && snapshot.ProgramComplete == snapshot.ProgramTotal
	})

	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoProgramSendAcceptedAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "accepted-mock-program.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load accepted program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start accepted program: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[GC:") &&
			countMockResponses(responses, "ok") >= 1
	})
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$G")
	})
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "program accepted-mock-program.gcode completed")
	})
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramCompleted && snapshot.ProgramComplete == snapshot.ProgramTotal
	})

	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoProgramSendUnsupportedLineAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "unsupported-mock-program.gcode", "G4 P0.1\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load unsupported program: %v", err)
	}

	requestStatus(t, controller)
	baseline := requireControllerIdle(t, controller)
	baselineX := baseline.MachinePosition[0]
	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start unsupported program: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "error:")
	})
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G4P0.1")
	})
	failed := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed && strings.Contains(snapshot.LastError, "program failed at line 1: error:")
	})
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "program failed") && hasControllerEventText(events, "program failed at line 1: error:")
	})

	state := m.state(t)
	if state.State != "Idle" || state.ActiveMove != nil || state.QueuedCommandCount != 0 || !near(state.MachinePosition[0], baselineX, posTol) {
		t.Fatalf("mock unsafe after unsupported program; failed=%+v; baselineX=%v; state=%+v", failed, baselineX, state)
	}

	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.ProgramStatus == app.ProgramFailed &&
			snapshot.HasMachinePosition &&
			snapshot.MachineState == "Idle" &&
			near(snapshot.MachinePosition[0], baselineX, posTol)
	})
}

func TestDDGoProgramAcksAreNotConfusedByStatusPollingAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	const pollingAckProgramLines = 50
	programPath := writeIntegrationProgramFile(
		t,
		"polling-ack-mock-program.gcode",
		repeatedProgramLine("$G", pollingAckProgramLines),
	)
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load polling ack program: %v", err)
	}

	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected &&
			snapshot.HasMachinePosition &&
			snapshot.LastStatusRaw != ""
	})

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start polling ack program: %v", err)
	}

	waitForNewMockResponses(t, m, responsesAfter, 10*time.Second, func(responses []mockLogEntry) bool {
		return countMockResponses(responses, "[GC:") >= pollingAckProgramLines &&
			countMockResponses(responses, "ok") >= pollingAckProgramLines
	})
	waitForNewMockEvents(t, m, eventsAfter, 10*time.Second, func(events []mockLogEntry) bool {
		return countMockEvents(events, "command", "$G") >= pollingAckProgramLines &&
			hasMockLogEntry(events, "command", "?")
	})
	h.waitForEventsAfter(t, controllerEventsAfter, 10*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "program polling-ack-mock-program.gcode completed")
	})
	waitForControllerState(t, controller, 10*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramCompleted &&
			snapshot.ProgramComplete == snapshot.ProgramTotal &&
			snapshot.ProgramTotal == pollingAckProgramLines
	})

	requestStatus(t, controller)
	requireControllerIdle(t, controller)
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

func writeRepeatedGStateProgram(t *testing.T, name string, count int) string {
	t.Helper()
	return writeIntegrationProgramFile(t, name, repeatedProgramLine("$G", count))
}

func writeIntegrationProgramFile(t *testing.T, name string, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write integration program: %v", err)
	}
	return path
}

func repeatedProgramLine(line string, count int) string {
	var b strings.Builder
	for i := 0; i < count; i++ {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func countMockResponses(entries []mockLogEntry, text string) int {
	count := 0
	for _, entry := range entries {
		if entry.Kind == "response" && strings.Contains(entry.Text, text) {
			count++
		}
	}
	return count
}

func countMockEvents(entries []mockLogEntry, kindContains, textContains string) int {
	count := 0
	for _, entry := range entries {
		if strings.Contains(entry.Kind, kindContains) && strings.Contains(entry.Text, textContains) {
			count++
		}
	}
	return count
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

func countControllerEventText(events []app.Event, text string) int {
	count := 0
	for _, event := range events {
		if strings.Contains(event.Text, text) {
			count++
		}
	}
	return count
}

func requireProgramErrorEvent(t *testing.T, h *controllerHarness, after int, text string) {
	t.Helper()
	h.waitForEventsAfter(t, after, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, text)
	})
}

func requireLoadedProgram(t *testing.T, c *app.Controller, name string, total int) {
	t.Helper()
	snapshot := c.Snapshot()
	if snapshot.ProgramStatus != app.ProgramLoaded ||
		snapshot.ProgramName != name ||
		snapshot.ProgramTotal != total ||
		snapshot.ProgramComplete != 0 ||
		snapshot.LastError != "" {
		t.Fatalf("loaded program state = %+v, want loaded %q with %d lines and no error", snapshot, name, total)
	}
}

func requireProgramCompleted(t *testing.T, c *app.Controller, total int) {
	t.Helper()
	waitForControllerState(t, c, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramCompleted &&
			snapshot.ProgramComplete == snapshot.ProgramTotal &&
			snapshot.ProgramTotal == total &&
			snapshot.LastError == ""
	})
}

func programStatusIsAny(status app.ProgramStatus, allowed ...app.ProgramStatus) bool {
	for _, candidate := range allowed {
		if status == candidate {
			return true
		}
	}
	return false
}

func hasControllerEventText(events []app.Event, text string) bool {
	for _, event := range events {
		if strings.Contains(event.Text, text) {
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
