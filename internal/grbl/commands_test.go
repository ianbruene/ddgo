package grbl

import (
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
