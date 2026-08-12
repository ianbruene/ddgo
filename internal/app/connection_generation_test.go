package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ianbruene/ddgo/internal/transport"
)

func TestGenerationStaleEventsCannotAffectReconnectedOwnerOrState(t *testing.T) {
	fake := transport.NewFakeTransport()
	c := NewController(fake, nil)
	c.statusPollInterval = time.Hour
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("A")); err != nil {
		t.Fatal(err)
	}
	generationA := fake.Generation()
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background(), transport.DefaultPortConfig("B")); err != nil {
		t.Fatal(err)
	}
	generationB := fake.Generation()
	if generationA == 0 || generationB == 0 || generationA == generationB {
		t.Fatalf("connection generations A=%d B=%d, want distinct non-zero values", generationA, generationB)
	}

	if err := c.SendConsoleLine(context.Background(), "G0 X1"); err != nil {
		t.Fatal(err)
	}
	waitForWrites(t, fake, 1)
	fake.InjectRXForGeneration(generationA, "ok")
	// A current-generation status is a FIFO barrier proving stale ok was seen.
	fake.InjectRXForGeneration(generationB, "<Idle|MPos:1,2,3>")
	waitForState(t, c, func(s State) bool { return s.MachineState == "Idle" })
	c.mu.RLock()
	owner := c.responseOwner
	c.mu.RUnlock()
	if owner.kind != responseOwnerManualLine || owner.generation != generationB {
		t.Fatalf("stale ok released current owner: %+v", owner)
	}

	fake.InjectRXForGeneration(generationA, "<Alarm|MPos:99,98,97>")
	fake.InjectErrorForGeneration(generationA, errors.New("stale read error"))
	fake.InjectRXForGeneration(generationB, "ok")
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.RLock()
		owner = c.responseOwner
		state := c.state
		c.mu.RUnlock()
		if owner.kind == responseOwnerNone {
			if state.MachineState != "Idle" || state.MachinePosition != [3]float64{1, 2, 3} || state.LastError != "" {
				t.Fatalf("stale status/error mutated B: %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("current-generation ok did not release owner")
		}
		time.Sleep(time.Millisecond)
	}

	fake.InjectDisconnectedForGeneration(generationA)
	fake.InjectRXForGeneration(generationB, "<Run|MPos:4,5,6>")
	waitForState(t, c, func(s State) bool { return s.Connected && s.MachineState == "Run" })
	c.mu.RLock()
	gotGeneration := c.connectionGeneration
	c.mu.RUnlock()
	if gotGeneration != generationB {
		t.Fatalf("generation after stale disconnect = %d, want %d", gotGeneration, generationB)
	}

	fake.InjectDisconnectedForGeneration(generationB)
	waitForState(t, c, func(s State) bool { return !s.Connected })
}

func TestGenerationExplicitDisconnectSuppressionIsIdentitySpecific(t *testing.T) {
	c := NewController(transport.NewFakeTransport(), nil)
	c.mu.Lock()
	c.connectionGeneration = 2
	c.state.Connected = true
	c.suppressTransportDisconnectedGeneration = 1
	c.mu.Unlock()

	if c.acceptDisconnectedEvent(1) {
		t.Fatal("explicit generation A disconnect was accepted")
	}
	if !c.acceptDisconnectedEvent(2) {
		t.Fatal("unexpected generation B disconnect was suppressed")
	}
}
