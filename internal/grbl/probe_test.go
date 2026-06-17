package grbl

import "testing"

func TestParseProbeResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		wantOK   bool
		wantPos  [3]float64
		wantSucc bool
	}{
		{name: "valid success", line: "[PRB:1.000,2.000,-3.500:1]", wantOK: true, wantPos: [3]float64{1, 2, -3.5}, wantSucc: true},
		{name: "valid no contact", line: "[PRB:1.000,2.000,-3.500:0]", wantOK: true, wantPos: [3]float64{1, 2, -3.5}, wantSucc: false},
		{name: "surrounding whitespace", line: " \t[PRB:1.000,2.000,-3.500:1]\n", wantOK: true, wantPos: [3]float64{1, 2, -3.5}, wantSucc: true},
		{name: "unrelated", line: "ok"},
		{name: "malformed coordinate count", line: "[PRB:1.000,2.000:1]"},
		{name: "malformed coordinate", line: "[PRB:1.000,nope,-3.500:1]"},
		{name: "non finite coordinate", line: "[PRB:1.000,+Inf,-3.500:1]"},
		{name: "missing status", line: "[PRB:1.000,2.000,-3.500]"},
		{name: "malformed status", line: "[PRB:1.000,2.000,-3.500:yes]"},
		{name: "unknown status", line: "[PRB:1.000,2.000,-3.500:2]"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseProbeResult(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ParseProbeResult() ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.Position != tt.wantPos {
				t.Fatalf("Position = %#v, want %#v", got.Position, tt.wantPos)
			}
			if got.Success != tt.wantSucc {
				t.Fatalf("Success = %v, want %v", got.Success, tt.wantSucc)
			}
		})
	}
}
