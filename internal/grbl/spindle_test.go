package grbl

import (
	"math"
	"strings"
	"testing"
)

func TestBuildSpindleCommands(t *testing.T) {
	builders := []struct {
		name, display string
		build         func() (string, string, error)
	}{
		{"CW", "S5000 M3", func() (string, string, error) {
			m, e := BuildSpindleStartCW(5000)
			return m.Display, string(m.Payload), e
		}},
		{"CCW", "S5000 M4", func() (string, string, error) {
			m, e := BuildSpindleStartCCW(5000)
			return m.Display, string(m.Payload), e
		}},
		{"speed", "S5000", func() (string, string, error) {
			m, e := BuildSpindleSpeed(5000)
			return m.Display, string(m.Payload), e
		}},
		{"stop", "M5", func() (string, string, error) { m := BuildSpindleStop(); return m.Display, string(m.Payload), nil }},
	}
	for _, tt := range builders {
		t.Run(tt.name, func(t *testing.T) {
			display, payload, err := tt.build()
			if err != nil {
				t.Fatal(err)
			}
			if display != tt.display || payload != tt.display+"\n" {
				t.Fatalf("got display %q payload %q", display, payload)
			}
		})
	}
}

func TestBuildSpindleRPMValidation(t *testing.T) {
	for _, rpm := range []float64{1500, 5000, 8000} {
		if _, err := BuildSpindleSpeed(rpm); err != nil {
			t.Errorf("RPM %v: %v", rpm, err)
		}
	}
	for _, rpm := range []float64{1499, 8001, 5000.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := BuildSpindleSpeed(rpm); err == nil || !strings.Contains(err.Error(), "spindle RPM") {
			t.Errorf("RPM %v error = %v", rpm, err)
		}
	}
}
