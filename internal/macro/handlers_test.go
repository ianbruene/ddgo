package macro

import (
	"context"
	"strings"
	"testing"

	"github.com/ianbruene/ddgo/internal/gcode"
)

func TestDefaultRegistration(t *testing.T) {
	RegisterDefaultHandlers(nil)
	if _, ok := NewRegistry().Handler(107); ok {
		t.Fatal("NewRegistry registered M107")
	}
	reg := NewDefaultRegistry()
	if _, ok := reg.Handler(107); !ok {
		t.Fatal("M107 not registered")
	}
	if _, ok := reg.Handler(108); !ok {
		t.Fatal("M108 not registered")
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
