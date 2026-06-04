package macro

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ianbruene/ddgo/internal/gcode"
)

type fakeRuntime struct {
	vars    *VariableStore
	contour *ContourState
	sent    []string
}

func (f *fakeRuntime) SendLineAndWaitOK(_ context.Context, line string) error {
	f.sent = append(f.sent, line)
	return nil
}
func (f *fakeRuntime) ReadWCSOffsets(context.Context) (WCSOffsets, error)       { return nil, nil }
func (f *fakeRuntime) WriteWCSOffset(context.Context, WCS, Axis, float64) error { return nil }
func (f *fakeRuntime) CurrentMachinePosition() (Point, bool)                    { return Point{}, false }
func (f *fakeRuntime) CurrentWorkPosition() (Point, bool)                       { return Point{}, false }
func (f *fakeRuntime) LastProbePoint() (Point, bool)                            { return Point{}, false }
func (f *fakeRuntime) RunProbe(context.Context, string) (Point, error)          { return Point{}, nil }
func (f *fakeRuntime) Variables() *VariableStore                                { return f.vars }
func (f *fakeRuntime) Contour() *ContourState                                   { return f.contour }

func TestParseInvocation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		line  gcode.Line
		code  int
		raw   string
		clean string
		ok    bool
	}{
		{"bare", gcode.Line{Raw: "M3", Text: "M3"}, 3, "", "", true},
		{"args", gcode.Line{Raw: "M5 S0 ; off", Text: "M5 S0"}, 5, "S0 ; off", "S0", true},
		{"generic", gcode.Line{Raw: "M999 X1 Y2", Text: "M999 X1 Y2"}, 999, "X1 Y2", "X1 Y2", true},
		{"lowercase", gcode.Line{Raw: "m42 P1", Text: "m42 P1"}, 42, "P1", "P1", true},
		{"compact", gcode.Line{Raw: "M999X1Y2", Text: "M999X1Y2"}, 999, "X1Y2", "X1Y2", true},
		{"embedded", gcode.Line{Raw: "G1 X1 M7", Text: "G1 X1 M7"}, 0, "", "", false},
		{"malformed", gcode.Line{Raw: "M X1", Text: "M X1"}, 0, "", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inv, ok := ParseInvocation(tt.line)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if inv.Code != tt.code || inv.RawArgs != tt.raw || inv.CleanArgs != tt.clean {
				t.Fatalf("inv = %+v", inv)
			}
		})
	}
}

func TestRegistryAndDispatch(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if _, ok := reg.Handler(7); ok {
		t.Fatal("empty registry returned handler")
	}

	called := false
	reg.Register(7, HandlerFunc(func(ctx context.Context, runtime Runtime, inv Invocation) error {
		called = true
		if inv.Code != 7 {
			t.Fatalf("code = %d", inv.Code)
		}
		return nil
	}))
	if _, ok := reg.Handler(7); !ok {
		t.Fatal("registered handler not found")
	}
	handled, err := NewEngine(reg).Dispatch(context.Background(), &fakeRuntime{}, gcode.Line{Number: 1, Raw: "M7", Text: "M7"})
	if err != nil || !handled || !called {
		t.Fatalf("handled=%v called=%v err=%v", handled, called, err)
	}

	handled, err = NewEngine(reg).Dispatch(context.Background(), &fakeRuntime{}, gcode.Line{Number: 2, Raw: "M8", Text: "M8"})
	if err != nil || handled {
		t.Fatalf("unregistered handled=%v err=%v", handled, err)
	}
}

func TestDispatchErrorPropagation(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	reg := NewRegistry()
	reg.Register(9, HandlerFunc(func(context.Context, Runtime, Invocation) error { return wantErr }))
	handled, err := NewEngine(reg).Dispatch(context.Background(), &fakeRuntime{}, gcode.Line{Number: 12, Raw: "M9", Text: "M9"})
	if !handled {
		t.Fatal("handled = false")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "M9") || !strings.Contains(err.Error(), "line 12") {
		t.Fatalf("error lacks context: %v", err)
	}
}

func TestVariableStore(t *testing.T) {
	t.Parallel()
	store := NewVariableStore()
	store.Set("a", 1.25)
	if got, ok := store.Get("a"); !ok || got != 1.25 {
		t.Fatalf("Get = %v,%v", got, ok)
	}
	snap := store.Snapshot()
	snap["a"] = 2
	if got, _ := store.Get("a"); got != 1.25 {
		t.Fatalf("snapshot mutated store: %v", got)
	}
	store.Delete("a")
	if _, ok := store.Get("a"); ok {
		t.Fatal("Get after Delete ok")
	}
	store.Set("b", 2)
	store.Clear()
	if got := store.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot after Clear = %#v", got)
	}
}

func TestContourState(t *testing.T) {
	t.Parallel()
	state := NewContourState()
	if err := state.AddPoint(Point{X: 1, Y: 2, Z: 3}); err != nil {
		t.Fatalf("AddPoint() error = %v", err)
	}
	if err := state.AddPoint(Point{X: 1, Y: 2, Z: 4}); err == nil {
		t.Fatal("duplicate AddPoint() error = nil")
	}
	if err := state.Enable(); err == nil {
		t.Fatal("Enable() with insufficient points error = nil")
	}
	if err := state.AddPoint(Point{X: 2, Y: 2, Z: 3}); err != nil {
		t.Fatal(err)
	}
	if err := state.AddPoint(Point{X: 3, Y: 2, Z: 3}); err != nil {
		t.Fatal(err)
	}
	if err := state.Enable(); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if !state.Enabled() {
		t.Fatal("Enabled() = false")
	}
	want := []Point{{X: 1, Y: 2, Z: 3}, {X: 2, Y: 2, Z: 3}, {X: 3, Y: 2, Z: 3}}
	if got := state.Points(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Points() = %#v", got)
	}
	state.Clear()
	if state.Enabled() || len(state.Points()) != 0 {
		t.Fatalf("Clear failed")
	}
}

func TestWCSHelpers(t *testing.T) {
	t.Parallel()
	cmd, err := BuildWCSWrite(WCS("G54"), AxisX, 1.5)
	if err != nil {
		t.Fatalf("BuildWCSWrite() error = %v", err)
	}
	if cmd != "G10 L2 P1 X1.500000" {
		t.Fatalf("cmd = %q", cmd)
	}
	offsets, err := ParseWCSOffsetsResponse([]string{"[G54:1.000,2.500,-3.000]", "[G55:0,0,0]"})
	if err != nil {
		t.Fatalf("ParseWCSOffsetsResponse() error = %v", err)
	}
	if offsets[WCS("G54")] != (Point{X: 1, Y: 2.5, Z: -3}) {
		t.Fatalf("offsets = %#v", offsets)
	}
}
