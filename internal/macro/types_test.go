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
	vars        *VariableStore
	noVars      bool
	contour     *ContourState
	sent        []string
	offsets     WCSOffsets
	offsetReads []WCSOffsets
	readErrs    []error
	writeErr    error
	probePoint  Point
	probeErr    error
	probeArgs   []string
	readWCS     int
	writes      []wcsWrite
}

type wcsWrite struct {
	wcs   WCS
	axis  Axis
	value float64
}

func (f *fakeRuntime) SendLineAndWaitOK(_ context.Context, line string) error {
	f.sent = append(f.sent, line)
	return nil
}
func (f *fakeRuntime) SendLineCollectingResponses(_ context.Context, line string) ([]string, error) {
	f.sent = append(f.sent, line)
	return nil, nil
}
func (f *fakeRuntime) ReadWCSOffsets(context.Context) (WCSOffsets, error) {
	f.readWCS++
	if len(f.readErrs) > 0 {
		err := f.readErrs[0]
		f.readErrs = f.readErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(f.offsetReads) > 0 {
		offsets := f.offsetReads[0]
		f.offsetReads = f.offsetReads[1:]
		return offsets, nil
	}
	return f.offsets, nil
}
func (f *fakeRuntime) WriteWCSOffset(_ context.Context, wcs WCS, axis Axis, value float64) error {
	f.writes = append(f.writes, wcsWrite{wcs: wcs, axis: axis, value: value})
	return f.writeErr
}
func (f *fakeRuntime) CurrentMachinePosition() (Point, bool) { return Point{}, false }
func (f *fakeRuntime) CurrentWorkPosition() (Point, bool)    { return Point{}, false }
func (f *fakeRuntime) LastProbePoint() (Point, bool)         { return Point{}, false }
func (f *fakeRuntime) RunProbe(_ context.Context, args string) (Point, error) {
	f.probeArgs = append(f.probeArgs, args)
	return f.probePoint, f.probeErr
}
func (f *fakeRuntime) Variables() *VariableStore {
	if f.noVars {
		return nil
	}
	if f.vars == nil {
		f.vars = NewVariableStore()
	}
	return f.vars
}
func (f *fakeRuntime) Contour() *ContourState { return f.contour }

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
		{"compact", gcode.Line{Raw: "M999X1Y2", Text: "M999X1Y2"}, 0, "", "", false},
		{"m107 args", gcode.Line{Raw: "M107 depth 1", Text: "M107 depth 1"}, 107, "depth 1", "depth 1", true},
		{"m107 bare", gcode.Line{Raw: "M107", Text: "M107"}, 107, "", "", true},
		{"m107 dotted", gcode.Line{Raw: "M107.1", Text: "M107.1"}, 0, "", "", false},
		{"m107 prefix", gcode.Line{Raw: "M107depth 1", Text: "M107depth 1"}, 0, "", "", false},
		{"m108 prefix", gcode.Line{Raw: "M108depth G54Z", Text: "M108depth G54Z"}, 0, "", "", false},
		{"m107 lowercase", gcode.Line{Raw: "m107 depth 1", Text: "m107 depth 1"}, 107, "depth 1", "depth 1", true},
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

func TestRegistryZeroValue(t *testing.T) {
	t.Parallel()
	var reg Registry

	reg.Register(42, HandlerFunc(func(context.Context, Runtime, Invocation) error {
		return nil
	}))

	handler, ok := reg.Handler(42)
	if !ok {
		t.Fatal("Handler ok = false")
	}
	if handler == nil {
		t.Fatal("Handler = nil")
	}

	reg.Register(43, nil)
	if handler, ok := reg.Handler(43); ok || handler != nil {
		t.Fatalf("nil handler registered: handler=%v ok=%v", handler, ok)
	}

	var nilReg *Registry
	nilReg.Register(44, HandlerFunc(func(context.Context, Runtime, Invocation) error {
		return nil
	}))
	if handler, ok := nilReg.Handler(44); ok || handler != nil {
		t.Fatalf("nil registry returned handler=%v ok=%v", handler, ok)
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

func TestNilHandlerFuncReturnsError(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	var handler HandlerFunc
	reg.Register(42, handler)

	handled, err := NewEngine(reg).Dispatch(
		context.Background(),
		&fakeRuntime{},
		gcode.Line{Number: 7, Raw: "M42", Text: "M42"},
	)

	if !handled {
		t.Fatal("handled = false")
	}
	if err == nil {
		t.Fatal("err = nil")
	}
	if !errors.Is(err, ErrNilHandlerFunc) {
		t.Fatalf("err = %v, want %v", err, ErrNilHandlerFunc)
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

func TestVariableStoreZeroValue(t *testing.T) {
	t.Parallel()
	var vars VariableStore

	vars.Set("foo", 12.5)
	value, ok := vars.Get("foo")
	if !ok {
		t.Fatal("Get ok = false")
	}
	if value != 12.5 {
		t.Fatalf("Get value = %v, want 12.5", value)
	}

	snap := vars.Snapshot()
	if got, ok := snap["foo"]; !ok || got != 12.5 {
		t.Fatalf("Snapshot[foo] = %v,%v; want 12.5,true", got, ok)
	}
	snap["foo"] = 99
	if got, _ := vars.Get("foo"); got != 12.5 {
		t.Fatalf("snapshot mutated store: %v", got)
	}

	vars.Delete("foo")
	if got, ok := vars.Get("foo"); ok {
		t.Fatalf("Get after Delete = %v,%v; want ok=false", got, ok)
	}

	vars.Delete("missing")
	vars.Set("bar", 3.25)
	vars.Clear()
	if got := vars.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot after Clear = %#v", got)
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

func TestContourStateZeroValue(t *testing.T) {
	t.Parallel()
	var contour ContourState

	if err := contour.AddPoint(Point{X: 1, Y: 2, Z: 3}); err != nil {
		t.Fatalf("AddPoint() error = %v", err)
	}
	if got := contour.Points(); !reflect.DeepEqual(got, []Point{{X: 1, Y: 2, Z: 3}}) {
		t.Fatalf("Points() = %#v", got)
	}
	if err := contour.Enable(); err == nil {
		t.Fatal("Enable() with insufficient points error = nil")
	}

	contour.Clear()
	if got := contour.Points(); len(got) != 0 {
		t.Fatalf("Points() after Clear = %#v", got)
	}
	if contour.Enabled() {
		t.Fatal("Enabled() after Clear = true")
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
