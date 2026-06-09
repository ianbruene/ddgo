package macro

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ianbruene/ddgo/internal/gcode"
)

type Axis string

const (
	AxisX Axis = "X"
	AxisY Axis = "Y"
	AxisZ Axis = "Z"
)

type WCS string

type Point struct {
	X float64
	Y float64
	Z float64
}

type WCSOffsets map[WCS]Point

type Invocation struct {
	Line      gcode.Line
	Code      int
	RawArgs   string
	CleanArgs string
}

type Runtime interface {
	SendLineAndWaitOK(ctx context.Context, line string) error

	ReadWCSOffsets(ctx context.Context) (WCSOffsets, error)
	WriteWCSOffset(ctx context.Context, wcs WCS, axis Axis, value float64) error

	CurrentMachinePosition() (Point, bool)
	CurrentWorkPosition() (Point, bool)

	LastProbePoint() (Point, bool)
	RunProbe(ctx context.Context, args string) (Point, error)

	Variables() *VariableStore
	Contour() *ContourState
}

type MotionRewriter interface {
	RewriteMotion(ctx context.Context, runtime Runtime, line gcode.Line) (string, bool, error)
}

var leadingMCodeRE = regexp.MustCompile(`^[mM]([0-9]+)(.*)$`)

func ParseInvocation(line gcode.Line) (Invocation, bool) {
	clean := strings.TrimSpace(line.Text)
	if clean == "" {
		return Invocation{}, false
	}
	cleanMatch := leadingMCodeRE.FindStringSubmatch(clean)
	if cleanMatch == nil {
		return Invocation{}, false
	}
	code, err := strconv.Atoi(cleanMatch[1])
	if err != nil {
		return Invocation{}, false
	}
	raw := strings.TrimSpace(line.Raw)
	if raw == "" {
		raw = clean
	}
	rawArgs := ""
	if rawMatch := leadingMCodeRE.FindStringSubmatch(raw); rawMatch != nil {
		rawArgs = strings.TrimSpace(rawMatch[2])
	}
	return Invocation{Line: line, Code: code, RawArgs: rawArgs, CleanArgs: strings.TrimSpace(cleanMatch[2])}, true
}

type Handler interface {
	HandleMacro(ctx context.Context, runtime Runtime, inv Invocation) error
}

type HandlerFunc func(ctx context.Context, runtime Runtime, inv Invocation) error

var ErrNilHandlerFunc = errors.New("nil macro handler function")

func (f HandlerFunc) HandleMacro(ctx context.Context, runtime Runtime, inv Invocation) error {
	if f == nil {
		return ErrNilHandlerFunc
	}
	return f(ctx, runtime, inv)
}

type Registry struct {
	mu       sync.RWMutex
	handlers map[int]Handler
}

func NewRegistry() *Registry { return &Registry{handlers: make(map[int]Handler)} }

func (r *Registry) ensureHandlersLocked() {
	if r.handlers == nil {
		r.handlers = make(map[int]Handler)
	}
}

func (r *Registry) Register(code int, handler Handler) {
	if r == nil || handler == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureHandlersLocked()
	r.handlers[code] = handler
}

func (r *Registry) Handler(code int) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[code]
	return h, ok
}

type Engine struct{ registry *Registry }

func NewEngine(registry *Registry) *Engine {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Engine{registry: registry}
}

func (e *Engine) Dispatch(ctx context.Context, runtime Runtime, line gcode.Line) (bool, error) {
	if e == nil {
		return false, nil
	}
	inv, ok := ParseInvocation(line)
	if !ok {
		return false, nil
	}
	h, ok := e.registry.Handler(inv.Code)
	if !ok {
		return false, nil
	}
	if err := h.HandleMacro(ctx, runtime, inv); err != nil {
		return true, &Error{LineNumber: line.Number, Code: inv.Code, Err: err}
	}
	return true, nil
}

type Error struct {
	LineNumber int
	Code       int
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "macro error"
	}
	return fmt.Sprintf("macro M%d failed at line %d: %v", e.Code, e.LineNumber, e.Err)
}
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type VariableStore struct {
	mu     sync.RWMutex
	values map[string]float64
}

func NewVariableStore() *VariableStore { return &VariableStore{values: make(map[string]float64)} }
func (s *VariableStore) ensureValuesLocked() {
	if s.values == nil {
		s.values = make(map[string]float64)
	}
}
func (s *VariableStore) Set(name string, value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureValuesLocked()
	s.values[name] = value
}
func (s *VariableStore) Get(name string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[name]
	return v, ok
}
func (s *VariableStore) Delete(name string) { s.mu.Lock(); defer s.mu.Unlock(); delete(s.values, name) }
func (s *VariableStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = make(map[string]float64)
}
func (s *VariableStore) Snapshot() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]float64, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out
}

type ContourState struct {
	mu      sync.RWMutex
	points  []Point
	enabled bool
}

func NewContourState() *ContourState { return &ContourState{} }
func (s *ContourState) AddPoint(p Point) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.points {
		if existing.X == p.X && existing.Y == p.Y {
			return fmt.Errorf("duplicate contour point at X=%g Y=%g", p.X, p.Y)
		}
	}
	s.points = append(s.points, p)
	return nil
}
func (s *ContourState) Points() []Point {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Point(nil), s.points...)
}
func (s *ContourState) Clear() { s.mu.Lock(); defer s.mu.Unlock(); s.points = nil; s.enabled = false }
func (s *ContourState) Enable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.points) < 3 {
		return fmt.Errorf("at least 3 contour points are required to define a surface")
	}
	s.enabled = true
	return nil
}
func (s *ContourState) Disable()      { s.mu.Lock(); defer s.mu.Unlock(); s.enabled = false }
func (s *ContourState) Enabled() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.enabled }

func BuildWCSWrite(wcs WCS, axis Axis, value float64) (string, error) {
	axis = Axis(strings.ToUpper(string(axis)))
	if axis != AxisX && axis != AxisY && axis != AxisZ {
		return "", fmt.Errorf("unsupported WCS axis %q", axis)
	}
	p, err := wcsPNumber(wcs)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("G10 L2 P%d %s%.6f", p, axis, value), nil
}

func wcsPNumber(wcs WCS) (int, error) {
	s := strings.ToUpper(strings.TrimSpace(string(wcs)))
	if strings.HasPrefix(s, "G") {
		n, err := strconv.Atoi(strings.TrimPrefix(s, "G"))
		if err != nil {
			return 0, fmt.Errorf("unsupported WCS %q", wcs)
		}
		if n >= 54 && n <= 59 {
			return n - 53, nil
		}
	}
	if strings.HasPrefix(s, "P") {
		n, err := strconv.Atoi(strings.TrimPrefix(s, "P"))
		if err == nil && n >= 1 && n <= 6 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("unsupported WCS %q", wcs)
}

var wcsRespRE = regexp.MustCompile(`^\[(G5[4-9]):([^\]]+)\]$`)

func ParseWCSOffsetsResponse(lines []string) (WCSOffsets, error) {
	out := make(WCSOffsets)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := wcsRespRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		parts := strings.Split(m[2], ",")
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid %s offset response: %q", m[1], line)
		}
		vals := [3]float64{}
		for i, part := range parts {
			v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("invalid %s offset value %q", m[1], part)
			}
			vals[i] = v
		}
		out[WCS(m[1])] = Point{X: vals[0], Y: vals[1], Z: vals[2]}
	}
	if len(out) == 0 {
		return nil, errors.New("no WCS offsets found")
	}
	return out, nil
}
