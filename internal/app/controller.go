package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"example.com/cncui/internal/grbl"
	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
)

type Controller struct {
	mu        sync.RWMutex
	transport transport.Transport
	listPorts ports.ListFunc
	events    chan Event
	state     State
}

func NewController(t transport.Transport, listPorts ports.ListFunc) *Controller {
	c := &Controller{
		transport: t,
		listPorts: listPorts,
		events:    make(chan Event, 256),
	}
	go c.runTransportEventBridge()
	return c
}

func (c *Controller) Events() <-chan Event {
	return c.events
}

func (c *Controller) Snapshot() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Controller) RefreshPorts(ctx context.Context) error {
	if c.listPorts == nil {
		err := errors.New("port lister is not configured")
		c.emitError(err)
		return err
	}
	list, err := c.listPorts(ctx)
	if err != nil {
		c.emitError(err)
		return err
	}
	c.events <- Event{Kind: EventPortsRefreshed, When: time.Now(), Ports: clonePorts(list), State: c.Snapshot()}
	return nil
}

func (c *Controller) Connect(ctx context.Context, cfg transport.PortConfig) error {
	if cfg.Name == "" {
		err := errors.New("port name is required")
		c.emitError(err)
		return err
	}
	if cfg.BaudRate <= 0 {
		err := errors.New("baud rate must be greater than zero")
		c.emitError(err)
		return err
	}
	if err := c.transport.Open(ctx, cfg); err != nil {
		c.emitError(err)
		return err
	}

	c.mu.Lock()
	c.state.Connected = true
	c.state.PortName = cfg.Name
	c.state.LastError = ""
	state := c.state
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: fmt.Sprintf("connected to %s", cfg.Name)}
	return nil
}

func (c *Controller) Disconnect() error {
	if err := c.transport.Close(); err != nil {
		c.emitError(err)
		return err
	}

	c.mu.Lock()
	c.state.Connected = false
	c.state.MachineState = ""
	state := c.state
	c.mu.Unlock()

	c.events <- Event{Kind: EventStateChanged, When: time.Now(), State: state, Text: "disconnected"}
	return nil
}

func (c *Controller) SendConsoleLine(ctx context.Context, line string) error {
	if err := c.transport.Write(ctx, transport.NewLineMessage(line)); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) Jog(ctx context.Context, axis string, delta float64, feed float64) error {
	msg, err := grbl.BuildJog(axis, delta, feed)
	if err != nil {
		c.emitError(err)
		return err
	}
	if err := c.transport.Write(ctx, msg); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) Action(ctx context.Context, action grbl.Action) error {
	msg, err := grbl.BuildAction(action)
	if err != nil {
		c.emitError(err)
		return err
	}
	if err := c.transport.Write(ctx, msg); err != nil {
		c.emitError(err)
		return err
	}
	return nil
}

func (c *Controller) runTransportEventBridge() {
	for ev := range c.transport.Events() {
		switch ev.Kind {
		case transport.EventConnected, transport.EventDisconnected:
			continue
		case transport.EventTX:
			c.events <- Event{Kind: EventConsoleTX, When: ev.When, Text: ev.Text, State: c.Snapshot(), Raw: ev}
		case transport.EventRX:
			c.mu.Lock()
			if ms := grbl.ParseMachineState(ev.Text); ms != "" {
				c.state.MachineState = ms
			}
			state := c.state
			c.mu.Unlock()
			c.events <- Event{Kind: EventConsoleRX, When: ev.When, Text: ev.Text, State: state, Raw: ev}
		case transport.EventError:
			c.emitError(ev.Err)
		}
	}
}

func (c *Controller) emitError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.state.LastError = err.Error()
	state := c.state
	c.mu.Unlock()
	c.events <- Event{Kind: EventError, When: time.Now(), Err: err, Text: err.Error(), State: state}
}

func clonePorts(list []ports.Info) []ports.Info {
	out := make([]ports.Info, len(list))
	copy(out, list)
	return out
}
