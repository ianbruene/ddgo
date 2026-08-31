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
	"github.com/ianbruene/ddgo/internal/gcode"
	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/macro"
	"github.com/ianbruene/ddgo/internal/transport"
)

type mockProcess struct {
	SerialPath string
	HTTPBase   string
	LogPath    string
	client     *http.Client

	cancel   context.CancelFunc
	cmd      *exec.Cmd
	stopOnce sync.Once
	waitErr  error
}

type mockState struct {
	State              string      `json:"state"`
	MachinePosition    [3]float64  `json:"machine_position"`
	ActiveMove         interface{} `json:"active_move"`
	QueuedCommandCount int         `json:"queued_command_count"`
	FreePlannerBlocks  int         `json:"free_planner_blocks"`
	QueueCapacity      int         `json:"queue_capacity"`
	LastErrorAlarm     string      `json:"last_error_alarm"`
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

type programQueryRewriter struct {
	fn func(context.Context, macro.Runtime, gcode.Line) (string, bool, error)
}

func (r programQueryRewriter) RewriteMotion(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
	return r.fn(ctx, runtime, line)
}

type mockGRBLOptions struct {
	ResponseDelay       time.Duration
	SuppressResponseFor string
	HoldResponseFor     string
	ProbeOmitResultFor  string
	StatusPositionField string
	StatusWCO           string
	StatusFS            string
}

func startMockGRBL(t *testing.T) *mockProcess {
	t.Helper()
	return startMockGRBLWithOptions(t, mockGRBLOptions{})
}

func startMockGRBLHoldingResponseFor(t *testing.T, command string) *mockProcess {
	t.Helper()
	return startMockGRBLWithOptions(t, mockGRBLOptions{HoldResponseFor: command})
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
	if opts.SuppressResponseFor != "" {
		args = append(args, "-suppress-response-for", opts.SuppressResponseFor)
	}
	if opts.HoldResponseFor != "" {
		args = append(args, "-hold-response-for", opts.HoldResponseFor)
	}
	if opts.ProbeOmitResultFor != "" {
		args = append(args, "-probe-omit-result-for", opts.ProbeOmitResultFor)
	}
	if opts.StatusPositionField != "" {
		args = append(args, "-status-position-field", opts.StatusPositionField)
	}
	if opts.StatusWCO != "" {
		args = append(args, "-status-wco", opts.StatusWCO)
	}
	if opts.StatusFS != "" {
		args = append(args, "-status-fs", opts.StatusFS)
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
		cancel:     cancel,
		cmd:        cmd,
	}

	t.Cleanup(func() {
		m.stop()
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

func (m *mockProcess) stop() error {
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		if m.cmd != nil {
			m.waitErr = m.cmd.Wait()
		}
	})
	return m.waitErr
}

func (m *mockProcess) stopNow(t *testing.T) {
	t.Helper()
	_ = m.stop()
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

func waitForNewMockEventsErr(m *mockProcess, after int, timeout time.Duration, pred func([]mockLogEntry) bool) error {
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
				return nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}

	return fmt.Errorf("new mock event condition not met within %s; after=%d; lastNew=%+v; lastAll=%+v; lastErr=%v", timeout, after, lastNew, lastAll, lastErr)
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

func assertNoNewMockResponseContainingFor(t *testing.T, m *mockProcess, after int, duration time.Duration, forbidden ...string) {
	t.Helper()

	deadline := time.Now().Add(duration)
	var lastNew []mockLogEntry
	var lastErr error

	for time.Now().Before(deadline) {
		var responses []mockLogEntry
		err := m.fetchJSON("/responses", &responses)
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}

		if after <= len(responses) {
			lastNew = responses[after:]
		} else {
			lastNew = nil
		}

		for _, entry := range lastNew {
			if entry.Kind != "response" {
				continue
			}
			for _, text := range forbidden {
				if strings.Contains(entry.Text, text) {
					t.Fatalf("forbidden mock response %q observed during %s; responses=%+v", text, duration, lastNew)
				}
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	if lastErr != nil && lastNew == nil {
		t.Fatalf("mock responses unavailable while checking forbidden responses during %s; lastErr=%v", duration, lastErr)
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

func TestMockGRBLDebugResetEndpoint(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)
	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", -10, 60); err != nil {
		t.Fatalf("start jog before debug reset: %v", err)
	}
	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.ActiveMove != nil
	})

	req, err := http.NewRequest(http.MethodPost, m.HTTPBase+"/reset", nil)
	if err != nil {
		t.Fatalf("create reset request: %v", err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		t.Fatalf("POST /reset: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /reset status = %s", resp.Status)
	}
	var resetLines []string
	if err := json.NewDecoder(resp.Body).Decode(&resetLines); err != nil {
		t.Fatalf("decode POST /reset response: %v", err)
	}
	resetText := strings.Join(resetLines, "")
	for _, want := range []string{"[MSG:reset]", "ALARM:3", "Grbl 1.1g [help:'$']"} {
		if !strings.Contains(resetText, want) {
			t.Fatalf("POST /reset response missing %q: %q", want, resetText)
		}
	}
	state := m.state(t)
	if state.State != "Idle" || state.ActiveMove != nil || state.QueuedCommandCount != 0 {
		t.Fatalf("debug reset state = %+v", state)
	}

	getResp, err := m.client.Get(m.HTTPBase + "/reset")
	if err != nil {
		t.Fatalf("GET /reset: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /reset status = %s, want 405", getResp.Status)
	}
}

func postMockHardLimit(t *testing.T, m *mockProcess, axis string) []string {
	t.Helper()
	path := m.HTTPBase + "/hard-limit"
	if axis != "" {
		path += "?axis=" + axis
	}
	req, err := http.NewRequest(http.MethodPost, path, nil)
	if err != nil {
		t.Fatalf("create hard-limit request: %v", err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		t.Fatalf("POST /hard-limit: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /hard-limit status = %s", resp.Status)
	}
	var lines []string
	if err := json.NewDecoder(resp.Body).Decode(&lines); err != nil {
		t.Fatalf("decode POST /hard-limit response: %v", err)
	}
	return lines
}

func TestMockGRBLDebugHardLimitEndpoint(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.JogTo(ctx, "X", -10, 60); err != nil {
		t.Fatalf("start jog: %v", err)
	}
	wip := waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.ActiveMove != nil && state.MachinePosition[0] < -0.01 && state.MachinePosition[0] > -9.99
	})
	text := strings.Join(postMockHardLimit(t, m, "X"), "")
	if !strings.Contains(text, "[MSG:Limit X]") || !strings.Contains(text, "ALARM:1") {
		t.Fatalf("hard-limit response = %q", text)
	}
	state := m.state(t)
	if state.State != "Alarm" || state.ActiveMove != nil || state.QueuedCommandCount != 0 ||
		state.FreePlannerBlocks != state.QueueCapacity || state.LastErrorAlarm != "ALARM:1" ||
		state.MachinePosition[0] >= 0 || state.MachinePosition[0] <= -10 || state.MachinePosition[0] > wip.MachinePosition[0] {
		t.Fatalf("hard-limit state = %+v; in-progress state=%+v", state, wip)
	}

	getResp, err := m.client.Get(m.HTTPBase + "/hard-limit?axis=X")
	if err != nil {
		t.Fatalf("GET /hard-limit: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /hard-limit status = %s", getResp.Status)
	}
	for _, path := range []string{"/hard-limit?axis=A", "/hard-limit"} {
		req, _ := http.NewRequest(http.MethodPost, m.HTTPBase+path, nil)
		resp, err := m.client.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("POST %s status = %s, want 400", path, resp.Status)
		}
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

func TestDDGoParsesMPosStatusAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{StatusPositionField: "MPos"})
	controller := connectControllerToMock(t, m)
	requestStatus(t, controller)

	snapshot := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition &&
			nearTriple(snapshot.MachinePosition, [3]float64{}, 0.001) && strings.Contains(snapshot.LastStatusRaw, "|MPos:")
	})
	if snapshot.HasWorkPosition || snapshot.HasWorkCoordinateOffset {
		t.Fatalf("MPos status unexpectedly set work fields: %+v", snapshot)
	}
}

func TestDDGoParsesWPosStatusAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{StatusPositionField: "WPos"})
	controller := connectControllerToMock(t, m)
	requestStatus(t, controller)

	snapshot := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasWorkPosition &&
			nearTriple(snapshot.WorkPosition, [3]float64{}, 0.001) && strings.Contains(snapshot.LastStatusRaw, "|WPos:")
	})
	if snapshot.HasMachinePosition || snapshot.HasWorkCoordinateOffset {
		t.Fatalf("WPos status unexpectedly set machine/WCO fields: %+v", snapshot)
	}
}

func TestDDGoParsesWPrimaryStatusAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{StatusPositionField: "W"})
	controller := connectControllerToMock(t, m)
	requestStatus(t, controller)

	snapshot := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasWorkPosition &&
			nearTriple(snapshot.WorkPosition, [3]float64{}, 0.001) && strings.Contains(snapshot.LastStatusRaw, "|W:")
	})
	if snapshot.HasMachinePosition || snapshot.HasWorkCoordinateOffset {
		t.Fatalf("primary W status unexpectedly set machine/WCO fields: %+v", snapshot)
	}
}

func TestDDGoParsesWCOAndFSStatusAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{
		StatusPositionField: "M",
		StatusWCO:           "1.000,2.000,-3.500",
		StatusFS:            "123,456",
	})
	controller := connectControllerToMock(t, m)
	requestStatus(t, controller)

	wantWCO := [3]float64{1, 2, -3.5}
	snapshot := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition &&
			snapshot.HasWorkCoordinateOffset && nearTriple(snapshot.WorkCoordinateOffset, wantWCO, 0.001) &&
			snapshot.HasFeedSpindle && near(snapshot.Feed, 123, 0.001) && near(snapshot.Spindle, 456, 0.001) &&
			strings.Contains(snapshot.LastStatusRaw, "|W:1.000,2.000,-3.500|") &&
			strings.Contains(snapshot.LastStatusRaw, "|FS:123,456|")
	})
	if !snapshot.Connected || snapshot.MachineState != "Idle" {
		t.Fatalf("controller not connected and idle: %+v", snapshot)
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

func TestDDGoProgramControlsRejectWithoutActiveRunAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	eventsAfter := mockEventCount(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	controllerEventsAfter := h.eventCount()
	err := controller.PauseProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	controllerEventsAfter = h.eventCount()
	err = controller.ResumeProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not paused")

	controllerEventsAfter = h.eventCount()
	err = controller.StopProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	snapshot := controller.Snapshot()
	if !snapshot.Connected || snapshot.ProgramStatus != app.ProgramNotLoaded || snapshot.MachineState != "Idle" {
		t.Fatalf("controller state after rejected program controls = %+v, want connected idle with no loaded program", snapshot)
	}
	assertNoProgramRealtimeFor(t, m, eventsAfter, 300*time.Millisecond)
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

func TestDDGoProgramControlsRejectAfterCompletionAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "controls-after-completion.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load controls after completion program: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start controls after completion program: %v", err)
	}
	requireProgramCompleted(t, controller, 1)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	before := controller.Snapshot()
	eventsAfter := mockEventCount(t, m)

	controllerEventsAfter := h.eventCount()
	err := controller.PauseProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	controllerEventsAfter = h.eventCount()
	err = controller.ResumeProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not paused")

	controllerEventsAfter = h.eventCount()
	err = controller.StopProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	snapshot := controller.Snapshot()
	if snapshot.ProgramStatus != app.ProgramCompleted ||
		snapshot.ProgramComplete != snapshot.ProgramTotal ||
		snapshot.ProgramName != before.ProgramName ||
		snapshot.ProgramPath != before.ProgramPath ||
		!snapshot.Connected ||
		snapshot.MachineState != "Idle" {
		t.Fatalf("controller state after rejected controls following completion = %+v, before=%+v", snapshot, before)
	}
	assertNoProgramRealtimeFor(t, m, eventsAfter, 300*time.Millisecond)
}

func TestDDGoProgramControlsRejectAfterFailureAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "controls-after-failure.gcode", "G4 P0.1\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load controls after failure program: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start controls after failure program: %v", err)
	}
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed &&
			strings.Contains(snapshot.LastError, "program failed at line 1: error:")
	})
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	before := controller.Snapshot()
	eventsAfter := mockEventCount(t, m)

	controllerEventsAfter := h.eventCount()
	err := controller.PauseProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	controllerEventsAfter = h.eventCount()
	err = controller.ResumeProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not paused")

	controllerEventsAfter = h.eventCount()
	err = controller.StopProgram(ctx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	snapshot := controller.Snapshot()
	if snapshot.ProgramStatus != app.ProgramFailed ||
		snapshot.LastError == "" ||
		snapshot.ProgramName != before.ProgramName ||
		snapshot.ProgramPath != before.ProgramPath ||
		!snapshot.Connected ||
		snapshot.MachineState != "Idle" {
		t.Fatalf("controller state after rejected controls following failure = %+v, before=%+v", snapshot, before)
	}
	assertNoProgramRealtimeFor(t, m, eventsAfter, 300*time.Millisecond)
}

func TestDDGoProgramControlsRejectAfterStopAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeRepeatedGStateProgram(t, "controls-after-stop.gcode", 50)
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load controls after stop program: %v", err)
	}

	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start controls after stop program: %v", err)
	}
	waitForActiveProgramProgress(t, controller, 5*time.Second)

	actionCtx, actionCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer actionCancel()
	if err := controller.StopProgram(actionCtx); err != nil {
		t.Fatalf("stop active controls after stop program: %v", err)
	}
	waitForProgramStatus(t, controller, 5*time.Second, app.ProgramStopped)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	waitForMockEvents(t, m, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "!") && hasMockLogEntry(events, "command", "Ctrl-X")
	})

	eventsAfter := mockEventCount(t, m)
	before := controller.Snapshot()

	controllerEventsAfter := h.eventCount()
	err := controller.PauseProgram(actionCtx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	controllerEventsAfter = h.eventCount()
	err = controller.ResumeProgram(actionCtx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not paused")

	controllerEventsAfter = h.eventCount()
	err = controller.StopProgram(actionCtx)
	requireProgramControlError(t, h, controllerEventsAfter, err, "program is not running")

	snapshot := controller.Snapshot()
	if snapshot.ProgramStatus != app.ProgramStopped ||
		snapshot.ProgramName != before.ProgramName ||
		snapshot.ProgramPath != before.ProgramPath ||
		!snapshot.Connected ||
		snapshot.MachineState != "Idle" {
		t.Fatalf("controller state after rejected controls following stop = %+v, before=%+v", snapshot, before)
	}
	assertNoProgramRealtimeFor(t, m, eventsAfter, 300*time.Millisecond)
}

func TestDDGoProgramFailsWhenAckIsMissingAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{SuppressResponseFor: "M5"})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "program-missing-ack.gcode", "M5\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load missing ack program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	eventsAfter := mockEventCount(t, m)
	responsesAfter := mockResponseCount(t, m)
	controllerEventsAfter := h.eventCount()

	runCtx, runCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start missing ack program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "M5")
	})
	assertNoNewMockResponseContainingFor(t, m, responsesAfter, 500*time.Millisecond, "[GC:", "ok")
	failed := requireProgramFailedWithError(t, controller, "context deadline exceeded")
	requireProgramErrorEvent(t, h, controllerEventsAfter, "context deadline exceeded")

	requestStatus(t, controller)
	idle := requireControllerIdle(t, controller)
	if !idle.Connected || idle.MachineState != "Idle" || idle.ProgramStatus != app.ProgramFailed {
		t.Fatalf("final status = %+v after failure %+v, want connected idle ProgramFailed", idle, failed)
	}
}

func TestDDGoProgramFailsWhenHardLimitOccursDuringAckWaitAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{SuppressResponseFor: "$G"})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programName := "program-hard-limit-ack-wait.gcode"
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, programName, "$G\n")); err != nil {
		t.Fatalf("load hard-limit ack-wait program: %v", err)
	}
	requireLoadedProgram(t, controller, programName, 1)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	eventsAfter, controllerEventsAfter := mockEventCount(t, m), h.eventCount()
	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start hard-limit ack-wait program: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$G")
	})
	postMockHardLimit(t, m, "X")
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventKindText(events, app.EventConsoleRX, "[MSG:Limit X]") &&
			hasControllerEventKindText(events, app.EventConsoleRX, "ALARM:1")
	})

	failed := requireProgramFailedWithError(t, controller, "program failed at line 1: ALARM:1")
	if failed.ProgramStatus != app.ProgramFailed || failed.ProgramComplete != 0 {
		t.Fatalf("hard-limit failure state = %+v, want failed with zero progress", failed)
	}
	requireProgramErrorEvent(t, h, controllerEventsAfter, "ALARM:1")
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return state.Connected && state.MachineState == "Alarm" && strings.Contains(state.LastError, "ALARM:1")
	})
	requireUnlockAndRecoveryProgram(t, controller, m, "recovery-after-ack-alarm.gcode")
}

func TestDDGoProgramFailsWhenHardLimitOccursDuringMacroQueryAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{SuppressResponseFor: "$#"})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programName := "program-hard-limit-macro-query.gcode"
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, programName, "M107 depth G54Z\n$G\n")); err != nil {
		t.Fatalf("load hard-limit macro-query program: %v", err)
	}
	requireLoadedProgram(t, controller, programName, 2)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	eventsAfter, controllerEventsAfter := mockEventCount(t, m), h.eventCount()
	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start hard-limit macro-query program: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$#")
	})
	postMockHardLimit(t, m, "Y")
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventKindText(events, app.EventConsoleRX, "[MSG:Limit Y]") &&
			hasControllerEventKindText(events, app.EventConsoleRX, "ALARM:1")
	})

	failed := requireProgramFailedWithError(t, controller, "query command failed: ALARM:1")
	if !strings.Contains(failed.LastError, "macro M107 failed at line 1") {
		t.Fatalf("macro alarm failure lacks macro context: %q", failed.LastError)
	}
	if failed.ProgramStatus != app.ProgramFailed || failed.ProgramComplete != 0 {
		t.Fatalf("macro alarm failure state = %+v, want failed with zero progress", failed)
	}
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "$G")
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return state.Connected && state.MachineState == "Alarm"
	})
	requireUnlockAndRecoveryProgram(t, controller, m, "recovery-after-query-alarm.gcode")
}

func TestDDGoProgramFailsWhenTransportDropsDuringAckWaitAgainstMock(t *testing.T) {
	m := startMockGRBLHoldingResponseFor(t, "$G")
	// Do not suppress the response here. HoldResponseFor lets mockgrbl generate
	// the otherwise-valid response but blocks the serial write until the process is
	// killed, making this a deterministic transport-drop case.
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "program-transport-drop-ack.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load transport drop ack program: %v", err)
	}

	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	runCtx, runCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start transport drop ack program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$G")
	})
	m.stopNow(t)

	requireProgramFailedWithAnyError(t, controller, transportDropErrorTexts()...)
	requireControllerErrorEventAny(t, h, controllerEventsAfter, transportDropErrorTexts()...)
}

func TestDDGoReconnectsAfterActiveProgramMockProcessExit(t *testing.T) {
	first := startMockGRBLHoldingResponseFor(t, "$G")
	// Do not suppress the response here. HoldResponseFor lets mockgrbl generate
	// the otherwise-valid response but blocks the serial write until the process is
	// killed, making this a deterministic transport-drop case.
	h := connectControllerToMockWithEvents(t, first)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "program-transport-drop-reconnect.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load transport drop reconnect program: %v", err)
	}

	eventsAfter := mockEventCount(t, first)
	controllerEventsAfter := h.eventCount()
	runCtx, runCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start transport drop reconnect program: %v", err)
	}
	waitForNewMockEvents(t, first, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$G")
	})
	// Killing the mock while it is holding the $G response should surface as an
	// unexpected transport disconnect. Do not call Controller.Disconnect() here;
	// this test verifies the bridge clears controller connection state itself.
	first.stopNow(t)
	failed := requireProgramFailedWithAnyError(t, controller, transportDropErrorTexts()...)
	if failed.ProgramComplete != 0 {
		t.Fatalf("ProgramComplete = %d, want 0 after transport loss", failed.ProgramComplete)
	}
	if !containsAny(failed.LastError, transportDropErrorTexts()...) {
		t.Fatalf("LastError = %q, want transport-drop text", failed.LastError)
	}
	requireControllerErrorEventAny(t, h, controllerEventsAfter, transportDropErrorTexts()...)

	disconnected := waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return !state.Connected &&
			state.MachineState == "" &&
			!state.HasMachinePosition &&
			state.LastStatusRaw == "" &&
			state.ProgramStatus == app.ProgramFailed
	})
	if !containsAny(disconnected.LastError, transportDropErrorTexts()...) {
		t.Fatalf("LastError after process exit = %q, want transport-drop text", disconnected.LastError)
	}
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventKindText(events, app.EventStateChanged, "transport disconnected")
	})

	second := startMockGRBL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := controller.Connect(ctx, transport.DefaultPortConfig(second.SerialPath)); err != nil {
		t.Fatalf("reconnect after transport drop: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return controller.Snapshot().Connected })
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	recoveryPath := writeIntegrationProgramFile(t, "program-transport-drop-recovery.gcode", "$I\n")
	if err := controller.LoadProgramFile(recoveryPath); err != nil {
		t.Fatalf("load transport drop recovery program: %v", err)
	}

	responsesAfter := mockResponseCount(t, second)
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer recoveryCancel()
	if err := controller.StartProgram(recoveryCtx); err != nil {
		t.Fatalf("start transport drop recovery program: %v", err)
	}
	waitForNewMockResponses(t, second, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
	requireProgramCompleted(t, controller, 1)
	requestStatus(t, controller)
	idle := requireControllerIdle(t, controller)
	if !idle.Connected || idle.MachineState != "Idle" || idle.LastError != "" {
		t.Fatalf("final recovery status = %+v, want connected idle with no error", idle)
	}
}

func TestDDGoReconnectsAfterIdleMockProcessExit(t *testing.T) {
	first := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, first)
	controller := h.Controller
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	eventsAfter := h.eventCount()

	first.stopNow(t)
	disconnected := waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return !state.Connected && state.MachineState == "" &&
			!state.HasMachinePosition && state.LastStatusRaw == ""
	})
	if disconnected.LastError != "" {
		t.Fatalf("LastError after idle transport loss = %q, want empty", disconnected.LastError)
	}
	h.waitForEventsAfter(t, eventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventKindText(events, app.EventStateChanged, "transport disconnected")
	})

	second := startMockGRBL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := controller.Connect(ctx, transport.DefaultPortConfig(second.SerialPath)); err != nil {
		t.Fatalf("reconnect controller to second mock: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	responsesAfter := mockResponseCount(t, second)
	if err := controller.SendConsoleLine(ctx, "$I"); err != nil {
		t.Fatalf("send $I after reconnect: %v", err)
	}
	waitForNewMockResponses(t, second, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
	if state := controller.Snapshot(); state.LastError != "" {
		t.Fatalf("LastError after reconnect = %q, want empty; state=%+v", state.LastError, state)
	}
}

func TestDDGoExplicitDisconnectDoesNotReportTransportDisconnectedAgainstMock(t *testing.T) {
	first := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, first)
	controller := h.Controller
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	eventsAfter := h.eventCount()

	if err := controller.Disconnect(); err != nil {
		t.Fatalf("explicit disconnect: %v", err)
	}
	disconnected := waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return !state.Connected && state.MachineState == "" &&
			!state.HasMachinePosition && state.LastStatusRaw == ""
	})
	if disconnected.LastError != "" {
		t.Fatalf("LastError after explicit disconnect = %q, want empty", disconnected.LastError)
	}
	events := h.waitForEventsAfter(t, eventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventKindText(events, app.EventStateChanged, "disconnected")
	})
	// Give the serial event bridge time to expose any duplicate transport-loss
	// event before inspecting the complete post-disconnect event slice.
	time.Sleep(100 * time.Millisecond)
	events = h.eventsAfter(eventsAfter)
	assertControllerEventTextCount(t, events, "disconnected", 1)
	assertControllerEventTextCount(t, events, "transport disconnected", 0)

	second := startMockGRBL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := controller.Connect(ctx, transport.DefaultPortConfig(second.SerialPath)); err != nil {
		t.Fatalf("reconnect after explicit disconnect: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	responsesAfter := mockResponseCount(t, second)
	if err := controller.SendConsoleLine(ctx, "$I"); err != nil {
		t.Fatalf("send $I after explicit disconnect reconnect: %v", err)
	}
	waitForNewMockResponses(t, second, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
}

func TestDDGoReconnectsAfterManualJogMockProcessExit(t *testing.T) {
	first := startMockGRBL(t)
	controller := connectControllerToMock(t, first)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := controller.JogTo(ctx, "X", -10, 60); err != nil {
		t.Fatalf("start jog against first mock: %v", err)
	}
	waitForMockState(t, first, 5*time.Second, func(state mockState) bool {
		return state.ActiveMove != nil && state.MachinePosition[0] < -0.01
	})

	first.stopNow(t)
	disconnected := waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return !state.Connected && state.MachineState == "" && !state.HasMachinePosition
	})
	if disconnected.LastError != "" {
		t.Fatalf("LastError after jog transport loss = %q, want empty", disconnected.LastError)
	}

	second := startMockGRBL(t)
	if err := controller.Connect(ctx, transport.DefaultPortConfig(second.SerialPath)); err != nil {
		t.Fatalf("reconnect controller to second mock: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	responsesAfter := mockResponseCount(t, second)
	if err := controller.JogTo(ctx, "X", -1, 60); err != nil {
		t.Fatalf("start jog against second mock: %v", err)
	}
	waitForNewMockResponses(t, second, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ok")
	})
	waitForMockState(t, second, 5*time.Second, func(state mockState) bool {
		return state.State == "Jog" || (state.ActiveMove == nil && near(state.MachinePosition[0], -1, posTol))
	})
	requestStatus(t, controller)
	final := waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return state.Connected && state.MachineState != "Alarm" && state.HasMachinePosition
	})
	if final.LastError != "" {
		t.Fatalf("LastError after second jog = %q, want empty; state=%+v", final.LastError, final)
	}
}

func TestDDGoProgramTimeoutFailureThenSuccessfulRunAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{SuppressResponseFor: "$G"})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	failingPath := writeIntegrationProgramFile(t, "program-timeout-failure.gcode", "$G\n")
	if err := controller.LoadProgramFile(failingPath); err != nil {
		t.Fatalf("load timeout failure program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	// Allow the program-owned $G to cross its pre-send fresh-status barrier before
	// the deliberately suppressed command response times out the run.
	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start timeout failure program: %v", err)
	}
	requireProgramFailedWithError(t, controller, "context deadline exceeded")
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	recoveryPath := writeIntegrationProgramFile(t, "program-timeout-recovery.gcode", "$I\n")
	if err := controller.LoadProgramFile(recoveryPath); err != nil {
		t.Fatalf("load timeout recovery program: %v", err)
	}
	requireLoadedProgram(t, controller, "program-timeout-recovery.gcode", 1)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)

	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer recoveryCancel()
	if err := controller.StartProgram(recoveryCtx); err != nil {
		t.Fatalf("start timeout recovery program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$I")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
	requireProgramCompleted(t, controller, 1)
	requestStatus(t, controller)
	idle := requireControllerIdle(t, controller)
	if !idle.Connected || idle.MachineState != "Idle" || idle.LastError != "" {
		t.Fatalf("final recovery status = %+v, want connected idle with no error", idle)
	}
}

func TestDDGoProgramSystemCommandWaitsForPlannerIdleAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	path := writeIntegrationProgramFile(t, "system-command-barrier.gcode", "G1 X-10 F600\n$G\nM5\n")
	if err := controller.LoadProgramFile(path); err != nil {
		t.Fatalf("load barrier program: %v", err)
	}
	eventsAfter := mockEventCount(t, m)
	responsesAfter := mockResponseCount(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start barrier program: %v", err)
	}

	events := waitForNewMockEvents(t, m, eventsAfter, 10*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "motion_complete", "G1X-10F600") &&
			hasMockLogEntry(events, "command", "$G") && hasMockLogEntry(events, "command", "M5")
	})
	index := func(kind, text string, after int) int {
		for i, event := range events {
			if i > after && event.Kind == kind && event.Text == text {
				return i
			}
		}
		return -1
	}
	motion := index("command", "G1X-10F600", -1)
	accepted := index("response", "ok", motion)
	completed := index("motion_complete", "G1X-10F600", accepted)
	system := index("command", "$G", completed)
	following := index("command", "M5", system)
	if motion < 0 || accepted < 0 || completed < 0 || system < 0 || following < 0 {
		t.Fatalf("program causal order invalid: motion=%d accepted=%d completed=%d system=%d following=%d events=%+v",
			motion, accepted, completed, system, following, events)
	}
	// Mock history records when the PTY generates a status response, not when the
	// serial transport delivers it to DDGo. Do not infer barrier receive ordering
	// from the relative log positions of '?' or '<Idle|...>'; the focused fake-
	// transport tests control and assert that post-command invariant directly.
	responses := m.responses(t)[responsesAfter:]
	if hasMockResponse(responses, "[MSG:Busy]") || hasMockResponse(responses, "error:9") {
		t.Fatalf("barrier program triggered planner Busy response: %+v", responses)
	}
	requireProgramCompleted(t, controller, 3)
}

func TestDDGoProgramQueryFailsWhenTransportDropsAgainstMock(t *testing.T) {
	m := startMockGRBLHoldingResponseFor(t, "$#")
	// Do not suppress the response here. HoldResponseFor lets mockgrbl generate
	// the otherwise-valid response but blocks the serial write until the process is
	// killed, making this a deterministic transport-drop case.
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	controller.SetMotionRewriter(programQueryRewriter{fn: func(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
		_, err := runtime.ReadWCSOffsets(ctx)
		return line.Text, false, err
	}})

	programPath := writeIntegrationProgramFile(t, "program-query-transport-drop.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load query transport drop program: %v", err)
	}

	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	runCtx, runCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start query transport drop program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$#")
	})
	m.stopNow(t)

	failed := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed &&
			containsAny(snapshot.LastError, transportDropErrorTexts()...)
	})
	requireControllerErrorEventAny(t, h, controllerEventsAfter, transportDropErrorTexts()...)
	if failed.ProgramComplete != 0 {
		t.Fatalf("program completed %d lines after query transport drop; want 0; state=%+v", failed.ProgramComplete, failed)
	}
	if failed.Connected {
		if err := controller.Disconnect(); err != nil {
			t.Fatalf("disconnect after query transport drop: %v", err)
		}
	}
}

func TestDDGoProgramQueryFailsWhenResponseIsMissingAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{SuppressResponseFor: "$#"})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	controller.SetMotionRewriter(programQueryRewriter{fn: func(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
		_, err := runtime.ReadWCSOffsets(ctx)
		return line.Text, false, err
	}})

	programPath := writeIntegrationProgramFile(t, "program-query-missing-response.gcode", "M5\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load query missing response program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	// Allow the generated $# to cross its pre-send fresh-status barrier before
	// the deliberately suppressed command response times out the run.
	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start query missing response program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$#")
	})
	assertNoNewMockResponseContainingFor(t, m, responsesAfter, 500*time.Millisecond, "[G54:", "ok")
	failed := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed &&
			strings.Contains(snapshot.LastError, "rewrite line") &&
			strings.Contains(snapshot.LastError, "context deadline exceeded")
	})
	requireProgramErrorEvent(t, h, controllerEventsAfter, "context deadline exceeded")
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "M5")

	requestStatus(t, controller)
	idle := requireControllerIdle(t, controller)
	if idle.ProgramStatus != app.ProgramFailed {
		t.Fatalf("final status = %+v after failure %+v, want ProgramFailed", idle, failed)
	}
}

func TestDDGoProgramIgnoresSettingsDumpResponsesAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	programPath := writeIntegrationProgramFile(t, "noisy-settings.gcode", "$$\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load noisy settings program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start noisy settings program: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$$")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "$0=") && hasMockResponse(responses, "$132=") && hasMockResponse(responses, "ok")
	})
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "$0=") && hasControllerEventText(events, "$132=") && hasControllerEventText(events, "ok")
	})
	requireProgramCompleted(t, controller, 1)
	completed := controller.Snapshot()
	if completed.LastError != "" {
		t.Fatalf("LastError = %q, want empty", completed.LastError)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoProgramQueryIgnoresNoisyWCSResponsesAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	var offsets macro.WCSOffsets
	var queryErr error
	queryDone := make(chan struct{}, 1)
	controller.SetMotionRewriter(programQueryRewriter{fn: func(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
		offsets, queryErr = runtime.ReadWCSOffsets(ctx)
		queryDone <- struct{}{}
		return line.Text, false, queryErr
	}})

	programPath := writeIntegrationProgramFile(t, "program-query-wcs.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load program query WCS program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start program query WCS program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$#") && hasMockLogEntry(events, "command", "$G")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[MSG:wcs-dump]") && hasMockResponse(responses, "[G54:") && hasMockResponse(responses, "[GC:") && countMockResponses(responses, "ok") >= 2
	})
	select {
	case <-queryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("program query did not complete")
	}
	if queryErr != nil {
		t.Fatalf("ReadWCSOffsets() error = %v", queryErr)
	}
	if offsets == nil {
		t.Fatal("ReadWCSOffsets() returned nil offsets")
	}
	if got, ok := offsets[macro.WCS("G54")]; !ok || got != (macro.Point{}) {
		t.Fatalf("G54 offset = %+v, %v; want zero point", got, ok)
	}

	requireProgramCompleted(t, controller, 1)
	h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, "[MSG:wcs-dump]") && hasControllerEventText(events, "program program-query-wcs.gcode completed")
	})
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoProgramWritesWCSOffsetAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	controller.SetMotionRewriter(programQueryRewriter{fn: func(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
		if err := runtime.WriteWCSOffset(ctx, macro.WCS("G54"), macro.AxisZ, -1.25); err != nil {
			return line.Text, false, err
		}
		return line.Text, false, nil
	}})

	programPath := writeIntegrationProgramFile(t, "program-wcs-write.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load WCS write program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start WCS write program: %v", err)
	}

	events := waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G10L2P1Z-1.250000") && hasMockLogEntry(events, "command", "$G")
	})
	wcsIndex, queryIndex := -1, -1
	for i, event := range events {
		if event.Kind == "command" && event.Text == "G10L2P1Z-1.250000" {
			wcsIndex = i
		}
		if event.Kind == "command" && event.Text == "$G" {
			queryIndex = i
		}
	}
	if wcsIndex < 0 || queryIndex < 0 || wcsIndex >= queryIndex {
		t.Fatalf("WCS write did not precede program line: %+v", events)
	}
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[GC:") && countMockResponses(responses, "ok") >= 2
	})

	requireProgramCompleted(t, controller, 1)
	completed := controller.Snapshot()
	if completed.LastError != "" {
		t.Fatalf("LastError = %q, want empty", completed.LastError)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoProgramFailsWhenWCSWriteRejectedAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	controller.SetMotionRewriter(programQueryRewriter{fn: func(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
		err := runtime.SendLineAndWaitOK(ctx, "G10 L2 P7 Z1")
		return line.Text, false, err
	}})

	programPath := writeIntegrationProgramFile(t, "program-wcs-write-rejected.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load rejected WCS write program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start rejected WCS write program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G10L2P7Z1")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "error:20")
	})
	failed := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed &&
			strings.Contains(snapshot.LastError, "rewrite line") &&
			strings.Contains(snapshot.LastError, "macro command failed: error:20")
	})
	requireProgramErrorEvent(t, h, controllerEventsAfter, "macro command failed: error:20")
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "$G")

	requestStatus(t, controller)
	idle := requireControllerIdle(t, controller)
	if idle.ProgramStatus != app.ProgramFailed {
		t.Fatalf("final status = %+v after failure %+v, want ProgramFailed", idle, failed)
	}

	controller.SetMotionRewriter(nil)
	recoveryPath := writeIntegrationProgramFile(t, "program-after-wcs-rejection.gcode", "$I\n")
	if err := controller.LoadProgramFile(recoveryPath); err != nil {
		t.Fatalf("load recovery program: %v", err)
	}
	requireLoadedProgram(t, controller, "program-after-wcs-rejection.gcode", 1)

	recoveryResponsesAfter := mockResponseCount(t, m)
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer recoveryCancel()
	if err := controller.StartProgram(recoveryCtx); err != nil {
		t.Fatalf("start recovery program: %v", err)
	}
	waitForNewMockResponses(t, m, recoveryResponsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
	requireProgramCompleted(t, controller, 1)
	requestStatus(t, controller)
	final := requireControllerIdle(t, controller)
	if final.LastError != "" {
		t.Fatalf("LastError = %q after recovery, want empty", final.LastError)
	}
}

func TestDDGoProgramQueryFailureFailsProgramAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	controller.SetMotionRewriter(programQueryRewriter{fn: func(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
		_, err := runtime.SendLineCollectingResponses(ctx, "G4 P0.1")
		return line.Text, false, err
	}})

	programPath := writeIntegrationProgramFile(t, "program-query-fail.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load program query failure program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start program query failure program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G4P0.1")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "error:")
	})
	failed := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed &&
			strings.Contains(snapshot.LastError, "rewrite line") &&
			strings.Contains(snapshot.LastError, "query command failed: error:")
	})
	requireProgramErrorEvent(t, h, controllerEventsAfter, "query command failed: error:")
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "$G")

	requestStatus(t, controller)
	idle := requireControllerIdle(t, controller)
	if idle.ProgramStatus != app.ProgramFailed {
		t.Fatalf("final status = %+v after failure %+v, want ProgramFailed", idle, failed)
	}
}

func TestDDGoProgramQueryRequiresActiveRunAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	eventsAfter := mockEventCount(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := controller.SendLineCollectingResponses(ctx, "$#"); err == nil || !strings.Contains(err.Error(), "no active program run") {
		t.Fatalf("SendLineCollectingResponses() error = %v, want no active program run", err)
	}
	if _, err := controller.ReadWCSOffsets(ctx); err == nil || !strings.Contains(err.Error(), "no active program run") {
		t.Fatalf("ReadWCSOffsets() error = %v, want no active program run", err)
	}
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "$#")
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoConcurrentProgramQueriesRejectedAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ResponseDelay: 50 * time.Millisecond})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	eventsAfter := 0
	secondErrCh := make(chan error, 1)
	firstErrCh := make(chan error, 1)
	controller.SetMotionRewriter(programQueryRewriter{fn: func(ctx context.Context, runtime macro.Runtime, line gcode.Line) (string, bool, error) {
		go func() {
			_, err := runtime.SendLineCollectingResponses(ctx, "$#")
			firstErrCh <- err
		}()
		if err := waitForNewMockEventsErr(m, eventsAfter, 2*time.Second, func(events []mockLogEntry) bool {
			return hasMockLogEntry(events, "command", "$#")
		}); err != nil {
			return line.Text, false, err
		}
		_, secondErr := runtime.SendLineCollectingResponses(ctx, "$G")
		secondErrCh <- secondErr
		select {
		case firstErr := <-firstErrCh:
			if firstErr != nil {
				return line.Text, false, firstErr
			}
		case <-time.After(5 * time.Second):
			return line.Text, false, errors.New("first query did not finish")
		}
		return line.Text, false, nil
	}})

	programPath := writeIntegrationProgramFile(t, "program-query-concurrent.gcode", "$G\n")
	if err := controller.LoadProgramFile(programPath); err != nil {
		t.Fatalf("load concurrent query program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	eventsAfter = mockEventCount(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start concurrent query program: %v", err)
	}

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$#")
	})
	select {
	case secondErr := <-secondErrCh:
		if !errors.Is(secondErr, app.ErrProgramQueryActive) {
			t.Fatalf("second query error = %v, want ErrProgramQueryActive", secondErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second query did not complete")
	}
	requireProgramCompleted(t, controller, 1)
	events := waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return countMockEvents(events, "command", "$#") >= 1 &&
			countMockEvents(events, "command", "$G") >= 1
	})
	if got := countMockEvents(events, "command", "$#"); got != 1 {
		t.Fatalf("mock saw %d $# commands, want exactly one active query; events=%+v", got, events)
	}
	if got := countMockEvents(events, "command", "$G"); got != 1 {
		t.Fatalf("mock saw %d $G commands, want exactly one original program line; events=%+v", got, events)
	}
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

func TestDDGoDefaultM107M108NumericWritesWCSAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, "default-m107-m108-numeric.gcode", "M107 depth -1.25\nM108 depth G54Z\n$#\n")); err != nil {
		t.Fatalf("load numeric macro program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	responsesAfter, eventsAfter := mockResponseCount(t, m), mockEventCount(t, m)
	startLoadedControllerProgram(t, controller)
	events := waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G10L2P1Z-1.250000") && hasMockLogEntry(events, "command", "$#")
	})
	assertNoMockCommandPrefix(t, events, "M107", "M108")
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[G54:0.000,0.000,-1.250]") && hasMockResponse(responses, "ok")
	})
	requireProgramCompletedClean(t, controller, 3)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoDefaultM107M108CopyWCSAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMockWithEvents(t, m).Controller
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, "default-m107-m108-copy.gcode", "G10 L2 P1 Z-3.500000\nM107 depth G54Z\nM108 depth G55Z\n$#\n")); err != nil {
		t.Fatalf("load WCS copy macro program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	responsesAfter, eventsAfter := mockResponseCount(t, m), mockEventCount(t, m)
	startLoadedControllerProgram(t, controller)
	events := waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G10L2P1Z-3.500000") &&
			countMockEvents(events, "command", "$#") >= 2 &&
			hasMockLogEntry(events, "command", "G10L2P2Z-3.500000")
	})
	assertNoMockCommandPrefix(t, events, "M107", "M108")
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[G54:0.000,0.000,-3.500]") &&
			hasMockResponse(responses, "[G55:0.000,0.000,-3.500]")
	})
	requireProgramCompletedClean(t, controller, 4)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoDefaultM100WritesMidpointAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMockWithEvents(t, m).Controller
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, "default-m100-midpoint.gcode", "G10 L2 P1 X1.000000\nG10 L2 P2 X3.000000\nM100 G54X G55X G56X\nM5\n")); err != nil {
		t.Fatalf("load midpoint macro program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	responsesAfter, eventsAfter := mockResponseCount(t, m), mockEventCount(t, m)
	startLoadedControllerProgram(t, controller)
	events := waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockCommandSequence(events, "G10L2P1X1.000000", "G10L2P2X3.000000", "$#", "G10L2P3X2.000000", "$#")
	})
	assertNoMockCommandPrefix(t, events, "M100")
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[G56:2.000,0.000,0.000]")
	})
	requireProgramCompletedClean(t, controller, 4)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoDefaultM101PassAllowsNextLineAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMockWithEvents(t, m).Controller
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, "default-m101-pass.gcode", "G10 L2 P1 X1.000000\nG10 L2 P2 X1.005000\nM101 G54X G55X 0.010\n$G\n")); err != nil {
		t.Fatalf("load passing comparison program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	responsesAfter, eventsAfter := mockResponseCount(t, m), mockEventCount(t, m)
	startLoadedControllerProgram(t, controller)
	events := waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockCommandSequence(events, "G10L2P1X1.000000", "G10L2P2X1.005000", "$#", "$G")
	})
	assertNoMockCommandPrefix(t, events, "M101")
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[GC:") && hasMockResponse(responses, "ok")
	})
	requireProgramCompletedClean(t, controller, 4)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoDefaultM101FailurePreventsNextLineAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, "default-m101-failure.gcode", "G10 L2 P1 X1.000000\nG10 L2 P2 X1.010000\nM101 G54X G55X 0.001\n$G\n")); err != nil {
		t.Fatalf("load failing comparison program: %v", err)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
	eventsAfter, controllerEventsAfter := mockEventCount(t, m), h.eventCount()
	startLoadedControllerProgram(t, controller)
	events := waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockCommandSequence(events, "G10L2P1X1.000000", "G10L2P2X1.010000", "$#")
	})
	assertNoMockCommandPrefix(t, events, "M101")
	failed := requireProgramFailedWithError(t, controller, "WCS comparison failed")
	if !strings.Contains(failed.LastError, "macro M101 failed at line 3") {
		t.Fatalf("comparison failure lacks macro context: %q", failed.LastError)
	}
	if failed.ProgramComplete != 2 || failed.ProgramComplete == failed.ProgramTotal {
		t.Fatalf("failed program progress = %d/%d, want 2/4", failed.ProgramComplete, failed.ProgramTotal)
	}
	requireProgramErrorEvent(t, h, controllerEventsAfter, "WCS comparison failed")
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 300*time.Millisecond, "$G", "M101")
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	recoveryResponses := mockResponseCount(t, m)
	startLoadedProgram(t, controller, "m101-failure-recovery.gcode", "$I\n")
	waitForNewMockResponses(t, m, recoveryResponses, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
	requireProgramCompletedClean(t, controller, 1)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func hasMockCommandSequence(events []mockLogEntry, commands ...string) bool {
	next := 0
	for _, event := range events {
		if event.Kind == "command" && next < len(commands) && event.Text == commands[next] {
			next++
		}
	}
	return next == len(commands)
}

func startLoadedControllerProgram(t *testing.T, controller *app.Controller) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start program: %v", err)
	}
}

func assertNoMockCommandPrefix(t *testing.T, events []mockLogEntry, prefixes ...string) {
	t.Helper()
	for _, event := range events {
		if event.Kind != "command" {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(event.Text, prefix) {
				t.Fatalf("raw macro command with prefix %q reached mock: %+v", prefix, events)
			}
		}
	}
}

func installProbeMacro(t *testing.T, controller *app.Controller, command string, result chan<- macro.Point) {
	t.Helper()
	registry := macro.NewRegistry()
	registry.Register(92, macro.HandlerFunc(func(ctx context.Context, runtime macro.Runtime, _ macro.Invocation) error {
		point, err := runtime.RunProbe(ctx, command)
		if err != nil {
			return err
		}
		if result != nil {
			result <- point
		}
		return nil
	}))
	controller.SetMacroEngine(macro.NewEngine(registry))
}

func startLoadedProgram(t *testing.T, controller *app.Controller, name, program string) {
	t.Helper()
	if err := controller.LoadProgramFile(writeIntegrationProgramFile(t, name, program)); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	if err := controller.StartProgram(ctx); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
}

func TestDDGoRunProbeSucceedsAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMockWithEvents(t, m).Controller
	result := make(chan macro.Point, 1)
	installProbeMacro(t, controller, "G38.2 Z-5 F100", result)
	responsesAfter, eventsAfter := mockResponseCount(t, m), mockEventCount(t, m)
	startLoadedProgram(t, controller, "successful-probe.gcode", "M92\n")

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G38.2Z-5F100")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[PRB:0.000,0.000,-3.500:1]") && hasMockResponse(responses, "ok")
	})
	select {
	case point := <-result:
		if point != (macro.Point{X: 0, Y: 0, Z: -3.5}) {
			t.Fatalf("RunProbe point = %+v", point)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe macro did not return a point")
	}
	requireProgramCompleted(t, controller, 1)
	if point, ok := controller.LastProbePoint(); !ok || point != (macro.Point{X: 0, Y: 0, Z: -3.5}) {
		t.Fatalf("LastProbePoint = %+v, %v", point, ok)
	}
	if got := m.state(t).MachinePosition; got != [3]float64{0, 0, -3.5} {
		t.Fatalf("mock machine position = %v", got)
	}
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoRunProbeNoContactFailsAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMockWithEvents(t, m).Controller
	installProbeMacro(t, controller, "G38.2 Z-1 F100", nil)
	responsesAfter, eventsAfter := mockResponseCount(t, m), mockEventCount(t, m)
	startLoadedProgram(t, controller, "no-contact-probe.gcode", "M92\n$G\n")

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G38.2Z-1F100")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[PRB:0.000,0.000,-1.000:0]") && hasMockResponse(responses, "ok")
	})
	failed := requireProgramFailedWithError(t, controller, "probe did not contact")
	if !strings.Contains(failed.LastError, "macro M92 failed at line 1") {
		t.Fatalf("probe failure lacks macro context: %q", failed.LastError)
	}
	if point, ok := controller.LastProbePoint(); ok {
		t.Fatalf("failed probe stored LastProbePoint %+v", point)
	}
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 250*time.Millisecond, "$G")
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoRunProbeControllerErrorFailsAndRecoversAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMockWithEvents(t, m).Controller
	installProbeMacro(t, controller, "G38.2 Z-5 F0", nil)
	responsesAfter, eventsAfter := mockResponseCount(t, m), mockEventCount(t, m)
	startLoadedProgram(t, controller, "rejected-probe.gcode", "M92\n$G\n")

	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G38.2Z-5F0")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "error:20")
	})
	failed := requireProgramFailedWithError(t, controller, "query command failed: error:20")
	if !strings.Contains(failed.LastError, "macro M92 failed at line 1") {
		t.Fatalf("probe failure lacks macro context: %q", failed.LastError)
	}
	if point, ok := controller.LastProbePoint(); ok {
		t.Fatalf("rejected probe stored LastProbePoint %+v", point)
	}
	assertNoNewMockCommandContainingFor(t, m, eventsAfter, 250*time.Millisecond, "$G")

	recoveryResponses := mockResponseCount(t, m)
	startLoadedProgram(t, controller, "probe-error-recovery.gcode", "$I\n")
	waitForNewMockResponses(t, m, recoveryResponses, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
	requireProgramCompletedClean(t, controller, 1)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
}

func TestDDGoRunProbeMissingResultFailsAgainstMock(t *testing.T) {
	m := startMockGRBLWithOptions(t, mockGRBLOptions{ProbeOmitResultFor: "G38.2Z-5F100"})
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller
	controllerEventsAfter := h.eventCount()
	installProbeMacro(t, controller, "G38.2 Z-5 F100", nil)
	eventsAfter := mockEventCount(t, m)
	startLoadedProgram(t, controller, "missing-result-probe.gcode", "M92\n")
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "G38.2Z-5F100")
	})
	failed := requireProgramFailedWithError(t, controller, "probe result not reported")
	if !strings.Contains(failed.LastError, "macro M92 failed at line 1") {
		t.Fatalf("probe failure lacks macro context: %q", failed.LastError)
	}
	if point, ok := controller.LastProbePoint(); ok {
		t.Fatalf("missing-result probe stored LastProbePoint %+v", point)
	}
	events := h.eventsAfter(controllerEventsAfter)
	assertControllerEventsContainText(t, events, "ok")
	assertControllerEventsDoNotContainText(t, events, "[PRB:")
}

func TestDDGoRunProbeFailurePreservesPreviousLastProbeAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMockWithEvents(t, m).Controller
	installProbeMacro(t, controller, "G38.2 Z-5 F100", nil)
	startLoadedProgram(t, controller, "probe-before-failure.gcode", "M92\n")
	requireProgramCompleted(t, controller, 1)
	want := macro.Point{X: 0, Y: 0, Z: -3.5}
	if point, ok := controller.LastProbePoint(); !ok || point != want {
		t.Fatalf("LastProbePoint after success = %+v, %v", point, ok)
	}

	installProbeMacro(t, controller, "G38.2 Z-1 F100", nil)
	startLoadedProgram(t, controller, "probe-after-success.gcode", "M92\n")
	requireProgramFailedWithError(t, controller, "probe did not contact")
	if point, ok := controller.LastProbePoint(); !ok || point != want {
		t.Fatalf("LastProbePoint after failure = %+v, %v; want %+v, true", point, ok, want)
	}
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
		repeatedProgramLine("M5", pollingAckProgramLines),
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
		return countMockResponses(responses, "ok") >= pollingAckProgramLines
	})
	waitForNewMockEvents(t, m, eventsAfter, 10*time.Second, func(events []mockLogEntry) bool {
		return countMockEvents(events, "command", "M5") >= pollingAckProgramLines &&
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

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
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

func TestDDGoAbsoluteJogResponseOwnershipAgainstMock(t *testing.T) {
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

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", queuedJogTarget, queuedJogFeed); err != nil {
		t.Fatalf("queue X absolute jog: %v", err)
	}
	if err := controller.JogTo(jogCtx, "Y", queuedJogTarget, queuedJogFeed); !errors.Is(err, app.ErrInteractiveCommandActive) {
		t.Fatalf("queue Y while X response pending error = %v, want %v", err, app.ErrInteractiveCommandActive)
	}
	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" && state.ActiveMove == nil && near(state.MachinePosition[0], queuedJogTarget, posTol)
	})
	if err := controller.JogTo(jogCtx, "Y", queuedJogTarget, queuedJogFeed); err != nil {
		t.Fatalf("queue Y absolute jog after X completion: %v", err)
	}
	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Idle" && state.ActiveMove == nil && near(state.MachinePosition[1], queuedJogTarget, posTol)
	})
	if err := controller.JogTo(jogCtx, "Z", queuedJogTarget, queuedJogFeed); err != nil {
		t.Fatalf("queue Z absolute jog after Y completion: %v", err)
	}

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

func TestDDGoHomeActionAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition
	})

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer jogCancel()
	if err := controller.JogTo(jogCtx, "X", -1, 60); err != nil {
		t.Fatalf("jog before home: %v", err)
	}
	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return !near(state.MachinePosition[0], 0, posTol)
	})
	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.ActiveMove == nil && state.QueuedCommandCount == 0 && near(state.MachinePosition[0], -1, posTol)
	})
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.MachineState == "Idle" && snapshot.HasMachinePosition && near(snapshot.MachinePosition[0], -1, posTol)
	})

	eventsAfter, responsesAfter := mockEventCount(t, m), mockResponseCount(t, m)
	homeCtx, homeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer homeCancel()
	if err := controller.Action(homeCtx, grbl.ActionHome); err != nil {
		t.Fatalf("home action: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$H")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ok")
	})

	requestStatus(t, controller)
	final := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition &&
			nearTriple(snapshot.MachinePosition, [3]float64{}, posTol) && snapshot.LastError == ""
	})
	if final.LastError != "" {
		t.Fatalf("home controller state = %+v", final)
	}
	mock := m.state(t)
	if mock.State != "Idle" || mock.ActiveMove != nil || mock.QueuedCommandCount != 0 ||
		!nearTriple(mock.MachinePosition, [3]float64{}, posTol) {
		t.Fatalf("home mock state = %+v", mock)
	}
}

func TestDDGoManualHoldResumeAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller

	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition
	})
	eventsAfter := mockEventCount(t, m)

	holdCtx, holdCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer holdCancel()
	if err := controller.Action(holdCtx, grbl.ActionHold); err != nil {
		t.Fatalf("manual hold: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 2*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "!")
	})
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Hold" && snapshot.HasMachinePosition
	})

	eventsAfter = mockEventCount(t, m)
	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer resumeCancel()
	if err := controller.Action(resumeCtx, grbl.ActionResume); err != nil {
		t.Fatalf("manual resume: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 2*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "~")
	})
	requestStatus(t, controller)
	final := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition
	})
	if final.LastError != "" {
		t.Fatalf("LastError = %q, want empty; state=%+v", final.LastError, final)
	}
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

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
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

func TestDDGoSoftResetReportsAlarmAndStartupAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition
	})

	responsesAfter := mockResponseCount(t, m)
	eventsAfter := mockEventCount(t, m)
	controllerEventsAfter := h.eventCount()
	resetCtx, resetCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer resetCancel()
	if err := controller.Action(resetCtx, grbl.ActionSoftReset); err != nil {
		t.Fatalf("soft reset: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "Ctrl-X")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[MSG:reset]") && hasMockResponse(responses, "ALARM:3") &&
			hasMockResponse(responses, "Grbl 1.1g [help:'$']")
	})
	controllerEvents := h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventKindText(events, app.EventConsoleRX, "[MSG:reset]") &&
			hasControllerEventKindText(events, app.EventConsoleRX, "ALARM:3") &&
			hasControllerEventKindText(events, app.EventConsoleRX, "Grbl 1.1g [help:'$']")
	})
	assertNoControllerEventKind(t, controllerEvents, app.EventError)

	requestStatus(t, controller)
	final := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition
	})
	if final.LastError != "" {
		t.Fatalf("post-reset LastError = %q, want empty; state=%+v", final.LastError, final)
	}
}

func TestDDGoSeesHardLimitAlarmAndUnlocksAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	h := connectControllerToMockWithEvents(t, m)
	controller := h.Controller
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return state.Connected && state.MachineState == "Idle" && state.HasMachinePosition
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.JogTo(ctx, "X", -10, 60); err != nil {
		t.Fatalf("start pre-limit jog: %v", err)
	}
	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.ActiveMove != nil && state.MachinePosition[0] < -0.01 && state.MachinePosition[0] > -9.99
	})
	responsesAfter := mockResponseCount(t, m)
	controllerEventsAfter := h.eventCount()
	postMockHardLimit(t, m, "X")
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[MSG:Limit X]") && hasMockResponse(responses, "ALARM:1")
	})

	// The status byte carries the debug-triggered unsolicited lines through the
	// real PTY before the alarm status report.
	requestStatus(t, controller)
	controllerEvents := h.waitForEventsAfter(t, controllerEventsAfter, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventKindText(events, app.EventConsoleRX, "[MSG:Limit X]") &&
			hasControllerEventKindText(events, app.EventConsoleRX, "ALARM:1")
	})
	assertNoControllerEventKind(t, controllerEvents, app.EventError)
	alarmed := waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return state.Connected && state.MachineState == "Alarm" && state.HasMachinePosition &&
			state.MachinePosition[0] < 0 && state.MachinePosition[0] > -10
	})
	if alarmed.LastError != "" {
		t.Fatalf("manual hard limit poisoned LastError: %+v", alarmed)
	}

	eventsAfter := mockEventCount(t, m)
	responsesAfter = mockResponseCount(t, m)
	if err := controller.JogTo(ctx, "X", -1, 60); err != nil {
		t.Fatalf("write alarmed jog: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$J=G53G90X-1.000F60")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[MSG:Busy]") && hasMockResponse(responses, "error:9")
	})

	eventsAfter, responsesAfter = mockEventCount(t, m), mockResponseCount(t, m)
	if err := controller.Action(ctx, grbl.ActionUnlock); err != nil {
		t.Fatalf("unlock after hard limit: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$X")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ok")
	})
	requestStatus(t, controller)
	waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return state.Connected && state.MachineState == "Idle"
	})

	responsesAfter = mockResponseCount(t, m)
	if err := controller.JogTo(ctx, "X", -1, 60); err != nil {
		t.Fatalf("post-unlock jog: %v", err)
	}
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ok")
	})
	waitForMockState(t, m, 5*time.Second, func(state mockState) bool {
		return state.State == "Jog" || (state.State == "Idle" && near(state.MachinePosition[0], -1, posTol))
	})
	requestStatus(t, controller)
	final := waitForControllerState(t, controller, 5*time.Second, func(state app.State) bool {
		return state.Connected && state.MachineState != "Alarm"
	})
	if final.LastError != "" {
		t.Fatalf("post-recovery LastError = %q", final.LastError)
	}
}

func TestDDGoUnlockAfterSoftResetAgainstMock(t *testing.T) {
	m := startMockGRBL(t)
	controller := connectControllerToMock(t, m)
	requestStatus(t, controller)
	baseline := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition
	})

	responsesAfter := mockResponseCount(t, m)
	resetCtx, resetCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer resetCancel()
	if err := controller.Action(resetCtx, grbl.ActionSoftReset); err != nil {
		t.Fatalf("soft reset before unlock: %v", err)
	}
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ALARM:3")
	})

	eventsAfter, responsesAfter := mockEventCount(t, m), mockResponseCount(t, m)
	unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer unlockCancel()
	if err := controller.Action(unlockCtx, grbl.ActionUnlock); err != nil {
		t.Fatalf("unlock after reset: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$X")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ok")
	})

	requestStatus(t, controller)
	final := waitForControllerState(t, controller, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.Connected && snapshot.MachineState == "Idle" && snapshot.HasMachinePosition
	})
	if final.LastError != "" || final.ProgramStatus != baseline.ProgramStatus || final.ProgramStatus.IsActive() {
		t.Fatalf("post-unlock controller state = %+v; baseline=%+v", final, baseline)
	}
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

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	jogCtx, jogCancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	// These lifecycle tests only need acknowledged program lines. Using $G here
	// would turn every line into an execution barrier and make their intended
	// long-running timing depend on status-poll cadence.
	return writeIntegrationProgramFile(t, name, repeatedProgramLine("M5", count))
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

func assertControllerEventsContainText(t *testing.T, events []app.Event, text string) {
	t.Helper()
	for _, event := range events {
		if strings.Contains(event.Text, text) {
			return
		}
	}
	t.Fatalf("controller events do not contain %q; events=%+v", text, events)
}

func assertControllerEventsDoNotContainText(t *testing.T, events []app.Event, text string) {
	t.Helper()
	for _, event := range events {
		if strings.Contains(event.Text, text) {
			t.Fatalf("controller events contain forbidden text %q; events=%+v", text, events)
		}
	}
}

func requireProgramErrorEvent(t *testing.T, h *controllerHarness, after int, text string) {
	t.Helper()
	h.waitForEventsAfter(t, after, 5*time.Second, func(events []app.Event) bool {
		return hasControllerEventText(events, text)
	})
}

func requireProgramControlError(t *testing.T, h *controllerHarness, after int, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("program control error = %v, want containing %q", err, want)
	}
	requireProgramErrorEvent(t, h, after, want)
}

func assertNoProgramRealtimeFor(t *testing.T, m *mockProcess, after int, duration time.Duration) {
	t.Helper()
	assertNoNewMockCommandContainingFor(t, m, after, duration, "!", "~", "Ctrl-X")
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

func transportDropErrorTexts() []string {
	return []string{
		"transport disconnected",
		"context deadline exceeded",
		"input/output error",
		"device not configured",
		"bad file descriptor",
		"file already closed",
		"use of closed file",
		"use of closed network connection",
	}
}

func containsAny(s string, texts ...string) bool {
	for _, text := range texts {
		if strings.Contains(s, text) {
			return true
		}
	}
	return false
}

func requireProgramFailedWithAnyError(t *testing.T, c *app.Controller, texts ...string) app.State {
	t.Helper()
	return waitForControllerState(t, c, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed && containsAny(snapshot.LastError, texts...)
	})
}

func requireControllerErrorEventAny(t *testing.T, h *controllerHarness, after int, texts ...string) {
	t.Helper()
	h.waitForEventsAfter(t, after, 5*time.Second, func(events []app.Event) bool {
		for _, event := range events {
			if containsAny(event.Text, texts...) {
				return true
			}
		}
		return false
	})
}

func requireProgramFailedWithError(t *testing.T, c *app.Controller, text string) app.State {
	t.Helper()
	return waitForControllerState(t, c, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramFailed &&
			strings.Contains(snapshot.LastError, text)
	})
}

func requireProgramCompleted(t *testing.T, c *app.Controller, total int) app.State {
	t.Helper()
	return waitForControllerState(t, c, 5*time.Second, func(snapshot app.State) bool {
		return snapshot.ProgramStatus == app.ProgramCompleted &&
			snapshot.ProgramComplete == snapshot.ProgramTotal &&
			snapshot.ProgramTotal == total &&
			snapshot.LastError == ""
	})
}

func requireProgramCompletedClean(t *testing.T, c *app.Controller, total int) app.State {
	t.Helper()
	state := requireProgramCompleted(t, c, total)
	if state.LastError != "" {
		t.Fatalf("LastError = %q, want empty after completed program", state.LastError)
	}
	return state
}

func requireUnlockAndRecoveryProgram(t *testing.T, controller *app.Controller, m *mockProcess, programName string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	eventsAfter, responsesAfter := mockEventCount(t, m), mockResponseCount(t, m)
	if err := controller.Action(ctx, grbl.ActionUnlock); err != nil {
		t.Fatalf("unlock after alarm: %v", err)
	}
	waitForNewMockEvents(t, m, eventsAfter, 5*time.Second, func(events []mockLogEntry) bool {
		return hasMockLogEntry(events, "command", "$X")
	})
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "ok")
	})
	requestStatus(t, controller)
	requireControllerIdle(t, controller)

	path := writeIntegrationProgramFile(t, programName, "$I\n")
	if err := controller.LoadProgramFile(path); err != nil {
		t.Fatalf("load recovery program: %v", err)
	}
	requireLoadedProgram(t, controller, programName, 1)

	responsesAfter = mockResponseCount(t, m)
	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer runCancel()
	if err := controller.StartProgram(runCtx); err != nil {
		t.Fatalf("start recovery program: %v", err)
	}
	waitForNewMockResponses(t, m, responsesAfter, 5*time.Second, func(responses []mockLogEntry) bool {
		return hasMockResponse(responses, "[grbl:") && hasMockResponse(responses, "ok")
	})
	requireProgramCompletedClean(t, controller, 1)
	requestStatus(t, controller)
	requireControllerIdle(t, controller)
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

func hasControllerEventKindText(events []app.Event, kind app.EventKind, text string) bool {
	for _, event := range events {
		if event.Kind == kind && strings.Contains(event.Text, text) {
			return true
		}
	}
	return false
}

func assertControllerEventTextCount(t *testing.T, events []app.Event, text string, want int) {
	t.Helper()
	got := 0
	for _, event := range events {
		if event.Text == text {
			got++
		}
	}
	if got != want {
		t.Fatalf("controller event text %q count = %d, want %d; events=%+v", text, got, want, events)
	}
}

func assertNoControllerEventKind(t *testing.T, events []app.Event, kind app.EventKind) {
	t.Helper()
	for _, event := range events {
		if event.Kind == kind {
			t.Fatalf("unexpected controller event kind %q: %+v", kind, events)
		}
	}
}

func near(got, want, tol float64) bool {
	if got < want {
		return want-got <= tol
	}
	return got-want <= tol
}

func nearTriple(got, want [3]float64, tol float64) bool {
	return near(got[0], want[0], tol) && near(got[1], want[1], tol) && near(got[2], want[2], tol)
}
