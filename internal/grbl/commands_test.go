package grbl

import (
	"math"
	"strings"
	"testing"
)

func TestBuildJog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		axis        string
		delta       float64
		feed        float64
		wantDisplay string
		wantPayload string
		wantErr     string
	}{
		{name: "happy path", axis: "x", delta: 1.25, feed: 500, wantDisplay: "$J=G91 X1.250 F500", wantPayload: "$J=G91 X1.250 F500\n"},
		{name: "negative distance", axis: "Y", delta: -0.1, feed: 250, wantDisplay: "$J=G91 Y-0.100 F250", wantPayload: "$J=G91 Y-0.100 F250\n"},
		{name: "unsupported axis", axis: "A", delta: 1, feed: 100, wantErr: "unsupported jog axis"},
		{name: "zero distance", axis: "X", delta: 0, feed: 100, wantErr: "jog distance must be non-zero"},
		{name: "nonpositive feed", axis: "Z", delta: 1, feed: 0, wantErr: "jog feed must be greater than zero"},
		{name: "nan distance", axis: "X", delta: math.NaN(), feed: 100, wantErr: "jog distance must be finite"},
		{name: "inf distance", axis: "X", delta: math.Inf(1), feed: 100, wantErr: "jog distance must be finite"},
		{name: "nan feed", axis: "X", delta: 1, feed: math.NaN(), wantErr: "jog feed must be finite"},
		{name: "inf feed", axis: "X", delta: 1, feed: math.Inf(1), wantErr: "jog feed must be finite"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			msg, err := BuildJog(tt.axis, tt.delta, tt.feed)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("BuildJog() error = nil, want substring %q", tt.wantErr)
				}
				if got := err.Error(); got == "" || !strings.Contains(got, tt.wantErr) {
					t.Fatalf("BuildJog() error = %q, want substring %q", got, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildJog() error = %v", err)
			}
			if got, want := msg.Display, tt.wantDisplay; got != want {
				t.Fatalf("Display = %q, want %q", got, want)
			}
			if got, want := string(msg.Payload), tt.wantPayload; got != want {
				t.Fatalf("Payload = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildMachineJog(t *testing.T) {
	t.Parallel()

	msg, err := BuildMachineJog("x", -300, 500)
	if err != nil {
		t.Fatalf("BuildMachineJog() error = %v", err)
	}
	if got, want := msg.Display, "$J=G53 G90 X-300.000 F500"; got != want {
		t.Fatalf("Display = %q, want %q", got, want)
	}
	if got, want := string(msg.Payload), "$J=G53 G90 X-300.000 F500\n"; got != want {
		t.Fatalf("Payload = %q, want %q", got, want)
	}
}

func TestBuildMachineJogRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		axis    string
		target  float64
		feed    float64
		wantErr string
	}{
		{name: "invalid axis", axis: "A", target: -300, feed: 500, wantErr: "unsupported jog axis"},
		{name: "nan target", axis: "X", target: math.NaN(), feed: 500, wantErr: "jog target must be finite"},
		{name: "inf target", axis: "X", target: math.Inf(1), feed: 500, wantErr: "jog target must be finite"},
		{name: "zero feed", axis: "X", target: -300, feed: 0, wantErr: "jog feed must be greater than zero"},
		{name: "negative feed", axis: "X", target: -300, feed: -1, wantErr: "jog feed must be greater than zero"},
		{name: "nan feed", axis: "X", target: -300, feed: math.NaN(), wantErr: "jog feed must be finite"},
		{name: "inf feed", axis: "X", target: -300, feed: math.Inf(1), wantErr: "jog feed must be finite"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := BuildMachineJog(tt.axis, tt.target, tt.feed)
			if err == nil {
				t.Fatalf("BuildMachineJog() error = nil, want substring %q", tt.wantErr)
			}
			if got := err.Error(); !strings.Contains(got, tt.wantErr) {
				t.Fatalf("BuildMachineJog() error = %q, want substring %q", got, tt.wantErr)
			}
		})
	}
}

func TestBuildAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		action      Action
		wantDisplay string
		wantPayload string
	}{
		{action: ActionUnlock, wantDisplay: "$X", wantPayload: "$X\n"},
		{action: ActionHome, wantDisplay: "$H", wantPayload: "$H\n"},
		{action: ActionHold, wantDisplay: "!", wantPayload: "!"},
		{action: ActionResume, wantDisplay: "~", wantPayload: "~"},
		{action: ActionStatus, wantDisplay: "?", wantPayload: "?"},
		{action: ActionSoftReset, wantDisplay: "Ctrl-X", wantPayload: string([]byte{0x18})},
		{action: ActionJogCancel, wantDisplay: "Jog Cancel", wantPayload: string([]byte{0x85})},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.action), func(t *testing.T) {
			t.Parallel()
			msg, err := BuildAction(tt.action)
			if err != nil {
				t.Fatalf("BuildAction() error = %v", err)
			}
			if got, want := msg.Display, tt.wantDisplay; got != want {
				t.Fatalf("Display = %q, want %q", got, want)
			}
			if got, want := string(msg.Payload), tt.wantPayload; got != want {
				t.Fatalf("Payload = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildAction_Unsupported(t *testing.T) {
	t.Parallel()

	_, err := BuildAction(Action("dance"))
	if err == nil {
		t.Fatal("BuildAction() error = nil, want non-nil")
	}
}

func TestParseMachineState(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"<Idle|MPos:0.000,0.000,0.000>": "Idle",
		"<Run>":                         "Run",
		" <Hold|FS:0,0> ":               "Hold",
		"ok":                            "",
		"<>":                            "",
		"<  >":                          "",
		"missing end>":                  "",
		"<missing start":                "",
	}
	for line, want := range cases {
		if got := ParseMachineState(line); got != want {
			t.Fatalf("ParseMachineState(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestParseStatusReport(t *testing.T) {
	t.Parallel()

	report, ok := ParseStatusReport("<Idle|MPos:0.000,1.000,-2.500|FS:0,0>")
	if !ok {
		t.Fatal("ParseStatusReport() ok=false, want true")
	}
	if got, want := report.State, "Idle"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if !report.HasMPos || report.MPos != [3]float64{0, 1, -2.5} {
		t.Fatalf("MPos parse mismatch: %+v", report)
	}
	if !report.HasFS || report.Feed != 0 || report.Spindle != 0 {
		t.Fatalf("FS parse mismatch: %+v", report)
	}

	report, ok = ParseStatusReport("<Run|WPos:10.000,20.000,-1.000|FS:500,12000>")
	if !ok || !report.HasWPos || !report.HasFS {
		t.Fatalf("WPos/FS parse failed: %+v ok=%v", report, ok)
	}
	if got, want := report.WPos, [3]float64{10, 20, -1}; got != want {
		t.Fatalf("WPos = %v, want %v", got, want)
	}
	if got, want := report.Feed, 500.0; got != want {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
	if got, want := report.Spindle, 12000.0; got != want {
		t.Fatalf("Spindle = %v, want %v", got, want)
	}
}

func TestParseStatusReportInvalid(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"ok", "error:2", "", "<>", "<Idle|MPos:1,2>", "<Idle|FS:100>", "<Idle|WPos:a,b,c>"} {
		if _, ok := ParseStatusReport(line); ok {
			t.Fatalf("ParseStatusReport(%q) ok=true, want false", line)
		}
	}
}
