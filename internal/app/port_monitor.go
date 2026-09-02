package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

const defaultPortMonitorInterval = time.Second

var autoConnectRetryDelays = [...]time.Duration{time.Second, 2 * time.Second, 5 * time.Second}

type machineIdentity string

type portMonitorState struct {
	lastPorts             []ports.Info
	hasScanned            bool
	suppressedIdentity    machineIdentity
	retryIdentity         machineIdentity
	retryAfter            time.Time
	retryStep             int
	lastAutoErrorIdentity machineIdentity
	lastAutoError         string
	lastEnumerationError  string
	cancel                context.CancelFunc
	done                  chan struct{}
}

// StartPortMonitoring starts the controller's single discovery lifecycle. It
// is idempotent. The first enumeration happens in the new goroutine before its
// first interval wait.
func (c *Controller) StartPortMonitoring(ctx context.Context) {
	c.mu.Lock()
	if c.portMonitor.cancel != nil {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.portMonitor.cancel, c.portMonitor.done = cancel, done
	interval := c.portMonitorInterval
	if interval <= 0 {
		interval = defaultPortMonitorInterval
	}
	c.mu.Unlock()
	go c.portMonitorLoop(ctx, done, interval)
}

// StopPortMonitoring cancels discovery and waits for its enumeration or open
// operation to return. It is safe to call more than once.
func (c *Controller) StopPortMonitoring() {
	c.mu.RLock()
	cancel, done := c.portMonitor.cancel, c.portMonitor.done
	c.mu.RUnlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (c *Controller) portMonitorLoop(ctx context.Context, done chan struct{}, interval time.Duration) {
	defer func() {
		c.mu.Lock()
		if c.portMonitor.done == done {
			c.portMonitor.cancel, c.portMonitor.done = nil, nil
		}
		c.mu.Unlock()
		close(done)
	}()
	for {
		if err := c.scanPorts(ctx, false); err != nil && ctx.Err() == nil {
			// Enumeration errors use the ordinary controller error channel. A scan
			// remains serialized, so at most one is reported per interval.
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (c *Controller) scanPorts(ctx context.Context, explicit bool) error {
	c.portScanMu.Lock()
	defer c.portScanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.listPorts == nil {
		err := errors.New("port lister is not configured")
		if explicit {
			c.emitError(err)
		}
		return err
	}
	list, err := c.listPorts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.mu.Lock()
			report := explicit || c.portMonitor.lastEnumerationError != err.Error()
			c.portMonitor.lastEnumerationError = err.Error()
			c.mu.Unlock()
			if report {
				c.emitError(err)
			}
		}
		return err
	}
	list = clonePorts(list)
	c.mu.Lock()
	c.portMonitor.lastEnumerationError = ""
	changed := !c.portMonitor.hasScanned || !samePortTopology(c.portMonitor.lastPorts, list)
	previous := clonePorts(c.portMonitor.lastPorts)
	if changed {
		c.portMonitor.lastPorts = clonePorts(list)
		c.portMonitor.hasScanned = true
	}
	oldCandidate, oldOK := selectMachinePort(previous)
	newCandidate, newOK := selectMachinePort(list)
	if oldOK && (!newOK || identityForPort(oldCandidate) != identityForPort(newCandidate)) {
		c.resetAutoRetryLocked()
	}
	if c.portMonitor.lastAutoErrorIdentity != "" && !containsMachineIdentity(list, c.portMonitor.lastAutoErrorIdentity) {
		c.portMonitor.lastAutoErrorIdentity, c.portMonitor.lastAutoError = "", ""
	}
	if c.portMonitor.suppressedIdentity != "" && !containsMachineIdentity(list, c.portMonitor.suppressedIdentity) {
		c.portMonitor.suppressedIdentity = ""
	}
	emit := explicit || changed
	var snapshot versionedState
	if emit {
		snapshot = c.captureEventStateLocked()
	}
	c.mu.Unlock()
	if emit {
		c.events <- Event{Kind: EventPortsRefreshed, When: c.portMonitorNow(), Text: formatPortChanges(previous, list, explicit), Ports: clonePorts(list), State: snapshot.state, StateRevision: snapshot.revision}
	}
	c.considerAutoConnect(ctx, list)
	return nil
}

func (c *Controller) considerAutoConnect(ctx context.Context, list []ports.Info) {
	p, ok := selectMachinePort(list)
	if !ok || ctx.Err() != nil {
		return
	}
	id := identityForPort(p)
	c.mu.RLock()
	now := c.portMonitorNow()
	blocked := c.state.Connected || c.portMonitor.suppressedIdentity == id ||
		(c.portMonitor.retryIdentity == id && now.Before(c.portMonitor.retryAfter))
	c.mu.RUnlock()
	if blocked {
		return
	}
	err := c.connect(ctx, transport.DefaultPortConfig(p.Name), false)
	c.mu.Lock()
	if err == nil {
		c.resetAutoRetryLocked()
		c.portMonitor.lastAutoErrorIdentity, c.portMonitor.lastAutoError = "", ""
		c.mu.Unlock()
		return
	}
	if errors.Is(err, ErrAlreadyConnected) || errors.Is(err, ErrConnectionTransition) || ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	report := c.portMonitor.lastAutoErrorIdentity != id || c.portMonitor.lastAutoError != err.Error()
	c.portMonitor.lastAutoErrorIdentity, c.portMonitor.lastAutoError = id, err.Error()
	if c.portMonitor.retryIdentity != id {
		c.portMonitor.retryStep = 0
	}
	delay := autoConnectRetryDelays[min(c.portMonitor.retryStep, len(autoConnectRetryDelays)-1)]
	c.portMonitor.retryIdentity = id
	c.portMonitor.retryAfter = c.portMonitorNow().Add(delay)
	if c.portMonitor.retryStep < len(autoConnectRetryDelays)-1 {
		c.portMonitor.retryStep++
	}
	c.mu.Unlock()
	if report {
		c.emitError(err)
	}
}

func (c *Controller) portMonitorNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Controller) resetAutoRetryLocked() {
	c.portMonitor.retryIdentity, c.portMonitor.retryAfter, c.portMonitor.retryStep = "", time.Time{}, 0
}

func (c *Controller) connectedMachineIdentityLocked() machineIdentity {
	for _, p := range c.portMonitor.lastPorts {
		if p.Name == c.state.PortName && isMachinePort(p) {
			return identityForPort(p)
		}
	}
	return ""
}

// selectMachinePort deliberately returns false for both no matches and an
// ambiguous set. Automatic connection is safe only for exactly one match.
func selectMachinePort(list []ports.Info) (ports.Info, bool) {
	var found ports.Info
	n := 0
	for _, p := range list {
		if isMachinePort(p) {
			found, n = p, n+1
		}
	}
	return found, n == 1
}

// Production GrblDD controller hardware tested on Linux is the Arduino Due
// native USB port (Arduino VID 2341, PID 003e). Its serial descriptor is not
// reliably populated, so VID/PID are the positive classification fields.
func isMachinePort(p ports.Info) bool {
	return p.IsUSB && strings.EqualFold(strings.TrimSpace(p.VID), "2341") && strings.EqualFold(strings.TrimSpace(p.PID), "003e")
}

func identityForPort(p ports.Info) machineIdentity {
	serial := strings.ToLower(strings.TrimSpace(p.SerialNumber))
	if serial != "" {
		return machineIdentity("usb:" + strings.ToLower(p.VID) + ":" + strings.ToLower(p.PID) + ":" + serial)
	}
	if p.IsUSB && strings.TrimSpace(p.VID) != "" && strings.TrimSpace(p.PID) != "" {
		return machineIdentity("usb:" + strings.ToLower(strings.TrimSpace(p.VID)) + ":" + strings.ToLower(strings.TrimSpace(p.PID)))
	}
	return machineIdentity("path:" + p.Name)
}

func serialOpenError(name string, err error) error {
	if err != nil && (errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrExist) == false && strings.Contains(strings.ToLower(err.Error()), "permission denied")) {
		return fmt.Errorf("serial port %s was found but DDGo does not have permission to open it; check Linux serial-device permissions or dialout-group membership: %w", name, err)
	}
	return err
}

func formatPortChanges(old, current []ports.Info, explicit bool) string {
	oldByName := make(map[string]ports.Info, len(old))
	for _, p := range old {
		oldByName[p.Name] = p
	}
	lines := make([]string, 0)
	for _, p := range current {
		if prior, ok := oldByName[p.Name]; explicit || !ok || !samePortTopology([]ports.Info{prior}, []ports.Info{p}) {
			lines = append(lines, fmt.Sprintf("port detected: %s USB=%t VID=%s PID=%s Serial=%q", p.Name, p.IsUSB, p.VID, p.PID, p.SerialNumber))
		}
		delete(oldByName, p.Name)
	}
	for name := range oldByName {
		lines = append(lines, "port removed: "+name)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func containsMachineIdentity(list []ports.Info, id machineIdentity) bool {
	for _, p := range list {
		if isMachinePort(p) && identityForPort(p) == id {
			return true
		}
	}
	return false
}

func samePortTopology(a, b []ports.Info) bool {
	key := func(p ports.Info) string {
		return p.Name + "\x00" + strings.ToLower(p.VID) + "\x00" + strings.ToLower(p.PID) + "\x00" + p.SerialNumber + "\x00" + string(rune(boolByte(p.IsUSB)))
	}
	keys := func(in []ports.Info) []string {
		out := make([]string, len(in))
		for i, p := range in {
			out[i] = key(p)
		}
		sort.Strings(out)
		return out
	}
	aa, bb := keys(a), keys(b)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}
