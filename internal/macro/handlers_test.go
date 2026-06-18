package macro

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ianbruene/ddgo/internal/gcode"
)

func TestDefaultRegistration(t *testing.T) {
	RegisterDefaultHandlers(nil)
	if _, ok := NewRegistry().Handler(100); ok {
		t.Fatal("NewRegistry registered M100")
	}
	if _, ok := NewRegistry().Handler(101); ok {
		t.Fatal("NewRegistry registered M101")
	}
	if _, ok := NewRegistry().Handler(102); ok {
		t.Fatal("NewRegistry registered M102")
	}
	if _, ok := NewRegistry().Handler(106); ok {
		t.Fatal("NewRegistry registered M106")
	}
	if _, ok := NewRegistry().Handler(107); ok {
		t.Fatal("NewRegistry registered M107")
	}
	if _, ok := NewRegistry().Handler(108); ok {
		t.Fatal("NewRegistry registered M108")
	}
	if _, ok := NewRegistry().Handler(109); ok {
		t.Fatal("NewRegistry registered M109")
	}
	if _, ok := NewRegistry().Handler(112); ok {
		t.Fatal("NewRegistry registered M112")
	}
	if _, ok := NewRegistry().Handler(111); ok {
		t.Fatal("NewRegistry registered M111")
	}
	if _, ok := NewRegistry().Handler(110); ok {
		t.Fatal("NewRegistry registered M110")
	}
	reg := NewDefaultRegistry()
	if _, ok := reg.Handler(100); !ok {
		t.Fatal("M100 not registered")
	}
	if _, ok := reg.Handler(101); !ok {
		t.Fatal("M101 not registered")
	}
	if _, ok := reg.Handler(102); !ok {
		t.Fatal("M102 not registered")
	}
	if _, ok := reg.Handler(106); !ok {
		t.Fatal("M106 not registered")
	}
	if _, ok := reg.Handler(107); !ok {
		t.Fatal("M107 not registered")
	}
	if _, ok := reg.Handler(108); !ok {
		t.Fatal("M108 not registered")
	}
	if _, ok := reg.Handler(109); !ok {
		t.Fatal("M109 not registered")
	}
	if _, ok := reg.Handler(112); !ok {
		t.Fatal("M112 not registered")
	}
	if _, ok := reg.Handler(111); !ok {
		t.Fatal("M111 not registered")
	}
	if _, ok := reg.Handler(110); !ok {
		t.Fatal("M110 not registered")
	}
	rt := &fakeRuntime{}
	handled, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Number: 1, Raw: "M107 depth 1.5", Text: "M107 depth 1.5"})
	if err != nil || !handled {
		t.Fatalf("M107 handled=%v err=%v", handled, err)
	}
	handled, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Number: 2, Raw: "M108 depth G54 X", Text: "M108 depth G54 X"})
	if err != nil || !handled {
		t.Fatalf("M108 handled=%v err=%v", handled, err)
	}
}

func TestM107StoresNumericValues(t *testing.T) {
	for _, line := range []string{"M107 depth 1", "M107 depth -2.5", "M107 depth .125"} {
		rt := &fakeRuntime{}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: line, Text: line})
		if err != nil {
			t.Fatalf("Dispatch(%q) error = %v", line, err)
		}
		if _, ok := rt.Variables().Get("depth"); !ok {
			t.Fatalf("depth not stored for %q", line)
		}
	}
}

func TestM107Errors(t *testing.T) {
	for _, line := range []string{"M107", "M107 1foo 1", "M107 depth nope", "M107 depth NaN", "M107 depth G60 X", "M107 depth G54 A"} {
		rt := &fakeRuntime{}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: line, Text: line})
		if err == nil {
			t.Fatalf("Dispatch(%q) error = nil", line)
		}
	}
}

func TestM107StoresWCSValue(t *testing.T) {
	rt := &fakeRuntime{offsets: WCSOffsets{"G54": {X: 1.25, Y: 2, Z: 3}}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M107 depth WCS G54 X", Text: "M107 depth WCS G54 X"})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if rt.readWCS != 1 {
		t.Fatalf("readWCS = %d, want 1", rt.readWCS)
	}
	if got, ok := rt.Variables().Get("depth"); !ok || got != 1.25 {
		t.Fatalf("depth = %v,%v", got, ok)
	}
}

func TestM107MissingWCSOffset(t *testing.T) {
	rt := &fakeRuntime{offsets: WCSOffsets{"G55": {X: 1}}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M107 depth G54X", Text: "M107 depth G54X"})
	if err == nil || !strings.Contains(err.Error(), "missing WCS offset") {
		t.Fatalf("err = %v", err)
	}
}

func TestM108WritesVariable(t *testing.T) {
	rt := &fakeRuntime{}
	rt.Variables().Set("depth", -1.5)
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M108 depth WCS G55 Z", Text: "M108 depth WCS G55 Z"})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if len(rt.writes) != 1 || rt.writes[0] != (wcsWrite{wcs: "G55", axis: AxisZ, value: -1.5}) {
		t.Fatalf("writes = %#v", rt.writes)
	}
	if len(rt.sent) != 0 {
		t.Fatalf("SendLineAndWaitOK called: %#v", rt.sent)
	}
}

func TestM108Errors(t *testing.T) {
	for _, line := range []string{"M108", "M108 1foo G54 X", "M108 missing G54 X", "M108 depth", "M108 depth G60 X", "M108 depth G54 A"} {
		rt := &fakeRuntime{}
		rt.Variables().Set("depth", 1)
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: line, Text: line})
		if err == nil {
			t.Fatalf("Dispatch(%q) error = nil", line)
		}
	}
}

func TestM107NumericDoesNotReadWCS(t *testing.T) {
	rt := &fakeRuntime{}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M107 depth -1.25", Text: "M107 depth -1.25"})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if rt.readWCS != 0 {
		t.Fatalf("readWCS = %d, want 0", rt.readWCS)
	}
}

func TestM107WCSParseErrorPrecedence(t *testing.T) {
	for _, line := range []string{"M107 depth G60 X", "M107 depth G54 A"} {
		rt := &fakeRuntime{}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: line, Text: line})
		if err == nil {
			t.Fatalf("Dispatch(%q) error = nil", line)
		}
		if strings.Contains(err.Error(), "invalid numeric value") {
			t.Fatalf("Dispatch(%q) error = %v, want WCS-specific error", line, err)
		}
	}
}

func TestDefaultHandlersMissingVariableStore(t *testing.T) {
	for _, line := range []string{"M107 depth 1", "M108 depth G54 X"} {
		rt := &fakeRuntime{noVars: true}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: line, Text: line})
		if err == nil || !strings.Contains(err.Error(), "variable store is not available") {
			t.Fatalf("Dispatch(%q) error = %v, want variable store error", line, err)
		}
	}
}

func TestM100WritesMidpointAndVerifies(t *testing.T) {
	rt := &fakeRuntime{offsetReads: []WCSOffsets{
		{"G54": {X: -2.5}, "G55": {Z: 1.5}},
		{"G56": {Y: -0.5}},
	}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M100 G54X G55Z G56Y", Text: "M100 G54X G55Z G56Y"})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if rt.readWCS != 2 {
		t.Fatalf("readWCS = %d, want 2", rt.readWCS)
	}
	if len(rt.writes) != 1 || rt.writes[0] != (wcsWrite{wcs: "G56", axis: AxisY, value: -0.5}) {
		t.Fatalf("writes = %#v", rt.writes)
	}
	if len(rt.sent) != 0 {
		t.Fatalf("sent = %#v", rt.sent)
	}
}

func TestM100DecimalMidpointWithSpacedRefs(t *testing.T) {
	rt := &fakeRuntime{offsetReads: []WCSOffsets{
		{"G54": {X: 1.25}, "G55": {X: 2.75}},
		{"G56": {Z: 2.0}},
	}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M100 WCS G54 X G55 X G56 Z", Text: "M100 WCS G54 X G55 X G56 Z"})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if got := rt.writes[0].value; got != 2.0 {
		t.Fatalf("midpoint = %v, want 2", got)
	}
}

func TestM100Errors(t *testing.T) {
	tests := []struct{ line, want string }{
		{"M100", "missing first source WCS axis"},
		{"M100 G54X", "missing second source WCS axis"},
		{"M100 G54X G55X", "missing destination WCS axis"},
		{"M100 G60X G55X G56X", "unsupported WCS"},
		{"M100 G54A G55X G56X", "unsupported WCS axis"},
		{"M100 G54X G55X G56X extra", "unexpected arguments"},
	}
	for _, tt := range tests {
		rt := &fakeRuntime{}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: tt.line, Text: tt.line})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("Dispatch(%q) error = %v, want %q", tt.line, err, tt.want)
		}
	}
}

func TestM100RuntimeErrors(t *testing.T) {
	rt := &fakeRuntime{offsets: WCSOffsets{"G55": {X: 1}, "G56": {X: 0}}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M100 G54X G55X G56X", Text: "M100 G54X G55X G56X"})
	if err == nil || !strings.Contains(err.Error(), "missing WCS offset") {
		t.Fatalf("missing offset err = %v", err)
	}
	rt = &fakeRuntime{offsetReads: []WCSOffsets{{"G54": {X: 1}, "G55": {X: 3}}}, writeErr: errors.New("write failed")}
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M100 G54X G55X G56X", Text: "M100 G54X G55X G56X"})
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("write err = %v", err)
	}
	rt = &fakeRuntime{offsetReads: []WCSOffsets{{"G54": {X: 1}, "G55": {X: 3}}, {"G56": {X: 2.1}}}}
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M100 G54X G55X G56X", Text: "M100 G54X G55X G56X"})
	if err == nil || !strings.Contains(err.Error(), "M100 verification failed") {
		t.Fatalf("verify err = %v", err)
	}
}

func TestM101Comparisons(t *testing.T) {
	tests := []struct {
		name string
		a, b float64
		tol  string
		fail bool
	}{
		{"equal", 1, 1, "0", false},
		{"within", 1, 1.001, "0.01", false},
		{"exact", 1, 1.01, "0.01", false},
		{"outside", 1, 1.01, "0.001", true},
		{"negative", -2.5, -2.4, "0.2", false},
		{"decimal", 0.125, 0.130, "0.004", true},
	}
	for _, tt := range tests {
		rt := &fakeRuntime{offsets: WCSOffsets{"G54": {X: tt.a}, "G55": {X: tt.b}}}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M101 G54X G55X " + tt.tol, Text: "M101 G54X G55X " + tt.tol})
		if tt.fail && (err == nil || !strings.Contains(err.Error(), "WCS comparison failed")) {
			t.Fatalf("%s err = %v, want comparison failure", tt.name, err)
		}
		if !tt.fail && err != nil {
			t.Fatalf("%s err = %v", tt.name, err)
		}
		if rt.readWCS != 1 {
			t.Fatalf("%s readWCS = %d, want 1", tt.name, rt.readWCS)
		}
	}
}

func TestM101Errors(t *testing.T) {
	tests := []struct{ line, want string }{
		{"M101", "missing first WCS axis"},
		{"M101 G54X", "missing second WCS axis"},
		{"M101 G54X G55X", "missing tolerance"},
		{"M101 G54X G55X abc", "invalid tolerance"},
		{"M101 G54X G55X -0.001", "negative tolerance"},
		{"M101 G60X G55X 0.1", "unsupported WCS"},
		{"M101 G54A G55X 0.1", "unsupported WCS axis"},
		{"M101 G54X G55X 0.1 extra", "unexpected arguments"},
	}
	for _, tt := range tests {
		rt := &fakeRuntime{}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: tt.line, Text: tt.line})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("Dispatch(%q) error = %v, want %q", tt.line, err, tt.want)
		}
	}
	rt := &fakeRuntime{offsets: WCSOffsets{"G55": {X: 1}}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M101 G54X G55X 0.1", Text: "M101 G54X G55X 0.1"})
	if err == nil || !strings.Contains(err.Error(), "missing WCS offset") {
		t.Fatalf("missing offset err = %v", err)
	}
	rt = &fakeRuntime{readErrs: []error{errors.New("read failed")}}
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M101 G54X G55X 0.1", Text: "M101 G54X G55X 0.1"})
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("read err = %v", err)
	}
}

func TestM102WritesExpressions(t *testing.T) {
	rt := &fakeRuntime{}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M102 G54Z = (1 + 2) * 3", Text: "M102 G54Z = (1 + 2) * 3"})
	if err != nil {
		t.Fatalf("M102 numeric error = %v", err)
	}
	if rt.readWCS != 0 {
		t.Fatalf("readWCS = %d, want 0", rt.readWCS)
	}
	if len(rt.writes) != 1 || rt.writes[0] != (wcsWrite{wcs: "G54", axis: AxisZ, value: 9}) {
		t.Fatalf("writes = %#v", rt.writes)
	}

	rt = &fakeRuntime{offsets: WCSOffsets{"G54": {Z: 1}, "G55": {Z: 5}}}
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M102 G56Z = (G54Z + G55Z) / 2", Text: "M102 G56Z = (G54Z + G55Z) / 2"})
	if err != nil {
		t.Fatalf("M102 WCS error = %v", err)
	}
	if rt.readWCS != 1 {
		t.Fatalf("readWCS = %d, want 1", rt.readWCS)
	}
	if len(rt.writes) != 1 || rt.writes[0] != (wcsWrite{wcs: "G56", axis: AxisZ, value: 3}) {
		t.Fatalf("writes = %#v", rt.writes)
	}

	rt = &fakeRuntime{}
	rt.Variables().Set("depth", 0.125)
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M102 G54X = depth + 1", Text: "M102 G54X = depth + 1"})
	if err != nil {
		t.Fatalf("M102 var error = %v", err)
	}
	if len(rt.writes) != 1 || rt.writes[0].value != 1.125 {
		t.Fatalf("writes = %#v", rt.writes)
	}
}

func TestM102VariableContainingWCSSubstringDoesNotReadWCS(t *testing.T) {
	rt := &fakeRuntime{}
	rt.Variables().Set("depthG54Z", 2)

	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{
		Raw:  "M102 G55X = depthG54Z + 1",
		Text: "M102 G55X = depthG54Z + 1",
	})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if rt.readWCS != 0 {
		t.Fatalf("readWCS = %d, want 0", rt.readWCS)
	}
	if len(rt.writes) != 1 || rt.writes[0] != (wcsWrite{wcs: "G55", axis: AxisX, value: 3}) {
		t.Fatalf("writes = %#v", rt.writes)
	}
}

func TestM102Errors(t *testing.T) {
	for _, tt := range []struct{ line, want string }{
		{"M102", "missing destination WCS axis"}, {"M102 G54Z", "missing expression"}, {"M102 G60Z = 1", "unsupported WCS"},
		{"M102 G54Z =", "missing expression"}, {"M102 G54Z = 1 / 0", "division by zero"}, {"M102 G54Z = missing", "unknown variable"}, {"M102 G54Z = 1e309", "non-finite"},
	} {
		_, err := NewDefaultEngine().Dispatch(context.Background(), &fakeRuntime{}, gcode.Line{Raw: tt.line, Text: tt.line})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%q error = %v, want %q", tt.line, err, tt.want)
		}
	}
	rt := &fakeRuntime{writeErr: errors.New("write failed")}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M102 G54Z = 1", Text: "M102 G54Z = 1"})
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("write error = %v", err)
	}
}

func TestM106Assertions(t *testing.T) {
	for _, line := range []string{"M106 1 < 2", "M106 2 <= 2", "M106 3 > 2", "M106 3 >= 3", "M106 3 == 3", "M106 3 != 4"} {
		rt := &fakeRuntime{}
		_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: line, Text: line})
		if err != nil {
			t.Fatalf("%q error = %v", line, err)
		}
		if rt.readWCS != 0 {
			t.Fatalf("%q readWCS = %d", line, rt.readWCS)
		}
	}
	rt := &fakeRuntime{offsets: WCSOffsets{"G54": {Z: 1}, "G55": {Z: 2}}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M106 G54Z <= G55Z", Text: "M106 G54Z <= G55Z"})
	if err != nil || rt.readWCS != 1 {
		t.Fatalf("WCS assertion err=%v readWCS=%d", err, rt.readWCS)
	}
	rt = &fakeRuntime{}
	rt.Variables().Set("depth", -1)
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M106 depth < 0", Text: "M106 depth < 0"})
	if err != nil {
		t.Fatalf("var assertion error = %v", err)
	}
}

func TestM106Errors(t *testing.T) {
	for _, tt := range []struct{ line, want string }{
		{"M106", "missing left operand"}, {"M106 1", "missing comparison operator"}, {"M106 1 <", "missing right operand"},
		{"M106 2 < 1", "assertion failed"}, {"M106 2 < 1 ERROR custom failure", "custom failure"}, {"M106 missing > 0", "unknown variable"}, {"M106 G54Z < G55Z", "missing WCS offset"},
	} {
		_, err := NewDefaultEngine().Dispatch(context.Background(), &fakeRuntime{offsets: WCSOffsets{"G54": {Z: 1}}}, gcode.Line{Raw: tt.line, Text: tt.line})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%q error=%v, want %q", tt.line, err, tt.want)
		}
	}
}

func TestM109CollectsContourPoint(t *testing.T) {
	rt := &fakeRuntime{contour: NewContourState(), probePoint: Point{X: 1, Y: 2, Z: -3.5}}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M109 G38.2 Z-5 F100 ; keep raw", Text: "M109 G38.2 Z-5 F100"})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if len(rt.probeArgs) != 1 || rt.probeArgs[0] != "G38.2 Z-5 F100 ; keep raw" {
		t.Fatalf("RunProbe args = %#v, want exact raw args", rt.probeArgs)
	}
	if got, want := rt.contour.Points(), []Point{{X: 1, Y: 2, Z: -3.5}}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("contour points = %#v, want %#v", got, want)
	}
}

func TestM109Errors(t *testing.T) {
	runErr := errors.New("probe failed")
	tests := []struct {
		name      string
		line      string
		rt        *fakeRuntime
		want      string
		wantCalls int
	}{
		{"missing command", "M109", &fakeRuntime{contour: NewContourState()}, "missing probe command", 0},
		{"probe error", "M109 G38.2 Z-5 F100", &fakeRuntime{contour: NewContourState(), probeErr: runErr}, "probe failed", 1},
		{"nil contour", "M109 G38.2 Z-5 F100", &fakeRuntime{probePoint: Point{X: 1, Y: 2, Z: 3}}, "contour state is not available", 1},
		{"duplicate", "M109 G38.2 Z-5 F100", func() *fakeRuntime {
			c := NewContourState()
			_ = c.AddPoint(Point{X: 1, Y: 2, Z: 0})
			return &fakeRuntime{contour: c, probePoint: Point{X: 1, Y: 2, Z: -3.5}}
		}(), "duplicate contour point", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDefaultEngine().Dispatch(context.Background(), tt.rt, gcode.Line{Raw: tt.line, Text: tt.line})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if len(tt.rt.probeArgs) != tt.wantCalls {
				t.Fatalf("RunProbe calls = %d, want %d", len(tt.rt.probeArgs), tt.wantCalls)
			}
		})
	}
}

func TestM110EnableContour(t *testing.T) {
	contour := NewContourState()
	for _, p := range []Point{{X: 1, Y: 1, Z: -1}, {X: 2, Y: 1, Z: -2}, {X: 3, Y: 1, Z: -3}} {
		if err := contour.AddPoint(p); err != nil {
			t.Fatalf("AddPoint() error = %v", err)
		}
	}
	rt := &fakeRuntime{contour: contour}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M110", Text: "M110"})
	if err != nil {
		t.Fatalf("Dispatch(M110) error = %v", err)
	}
	if !contour.Enabled() {
		t.Fatal("contour enabled = false, want true")
	}
}

func TestM110Errors(t *testing.T) {
	tests := []struct {
		name string
		line string
		rt   *fakeRuntime
		want string
	}{
		{"too few points", "M110", &fakeRuntime{contour: NewContourState()}, "at least 3 contour points"},
		{"nil contour", "M110", &fakeRuntime{}, "contour state is not available"},
		{"unexpected args", "M110 unexpected", &fakeRuntime{contour: NewContourState()}, "unexpected arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDefaultEngine().Dispatch(context.Background(), tt.rt, gcode.Line{Raw: tt.line, Text: tt.line})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Dispatch(%q) error = %v, want %q", tt.line, err, tt.want)
			}
		})
	}
}

func TestM111DisableContour(t *testing.T) {
	contour := NewContourState()
	for _, p := range []Point{{X: 1, Y: 1, Z: -1}, {X: 2, Y: 1, Z: -2}, {X: 3, Y: 1, Z: -3}} {
		if err := contour.AddPoint(p); err != nil {
			t.Fatalf("AddPoint() error = %v", err)
		}
	}
	if err := contour.Enable(); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	rt := &fakeRuntime{contour: contour}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M111", Text: "M111"})
	if err != nil {
		t.Fatalf("Dispatch(M111) error = %v", err)
	}
	if contour.Enabled() {
		t.Fatal("contour enabled = true, want false")
	}
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M111", Text: "M111"})
	if err != nil {
		t.Fatalf("second Dispatch(M111) error = %v", err)
	}
}

func TestM111Errors(t *testing.T) {
	for _, tt := range []struct {
		line, want string
		rt         *fakeRuntime
	}{
		{"M111", "contour state is not available", &fakeRuntime{}},
		{"M111 unexpected", "unexpected arguments", &fakeRuntime{contour: NewContourState()}},
	} {
		_, err := NewDefaultEngine().Dispatch(context.Background(), tt.rt, gcode.Line{Raw: tt.line, Text: tt.line})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("Dispatch(%q) error = %v, want %q", tt.line, err, tt.want)
		}
	}
}

func TestM112ClearContour(t *testing.T) {
	contour := NewContourState()
	for _, p := range []Point{{X: 1, Y: 1, Z: -1}, {X: 2, Y: 1, Z: -2}, {X: 3, Y: 1, Z: -3}} {
		if err := contour.AddPoint(p); err != nil {
			t.Fatalf("AddPoint() error = %v", err)
		}
	}
	if err := contour.Enable(); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	rt := &fakeRuntime{contour: contour}
	_, err := NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M112", Text: "M112"})
	if err != nil {
		t.Fatalf("Dispatch(M112) error = %v", err)
	}
	if got := contour.Points(); len(got) != 0 {
		t.Fatalf("Points() = %#v, want empty", got)
	}
	if contour.Enabled() {
		t.Fatal("contour enabled = true, want false")
	}
	_, err = NewDefaultEngine().Dispatch(context.Background(), rt, gcode.Line{Raw: "M112", Text: "M112"})
	if err != nil {
		t.Fatalf("empty Dispatch(M112) error = %v", err)
	}
}

func TestM112Errors(t *testing.T) {
	for _, tt := range []struct {
		line, want string
		rt         *fakeRuntime
	}{
		{"M112", "contour state is not available", &fakeRuntime{}},
		{"M112 unexpected", "unexpected arguments", &fakeRuntime{contour: NewContourState()}},
	} {
		_, err := NewDefaultEngine().Dispatch(context.Background(), tt.rt, gcode.Line{Raw: tt.line, Text: tt.line})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("Dispatch(%q) error = %v, want %q", tt.line, err, tt.want)
		}
	}
}
