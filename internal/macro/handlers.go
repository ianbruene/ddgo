package macro

import (
	"context"
	"fmt"
	"math"
	"strings"
)

func RegisterDefaultHandlers(registry *Registry) {
	if registry == nil {
		return
	}
	registry.Register(100, HandlerFunc(handleM100))
	registry.Register(101, HandlerFunc(handleM101))
	registry.Register(102, HandlerFunc(handleM102))
	registry.Register(106, HandlerFunc(handleM106))
	registry.Register(107, HandlerFunc(handleM107))
	registry.Register(108, HandlerFunc(handleM108))
	registry.Register(109, HandlerFunc(handleM109))
	registry.Register(110, HandlerFunc(handleM110))
	registry.Register(111, HandlerFunc(handleM111))
	registry.Register(112, HandlerFunc(handleM112))
}

func NewDefaultRegistry() *Registry { r := NewRegistry(); RegisterDefaultHandlers(r); return r }
func NewDefaultEngine() *Engine     { return NewEngine(NewDefaultRegistry()) }

const m100VerificationTolerance = 0.000001

func handleM100(ctx context.Context, runtime Runtime, inv Invocation) error {
	args, err := parseM100Args(inv.CleanArgs)
	if err != nil {
		return err
	}
	offsets, err := runtime.ReadWCSOffsets(ctx)
	if err != nil {
		return err
	}
	resolver := WCSResolver{Offsets: offsets}
	a, err := resolver.Resolve(args.SourceA)
	if err != nil {
		return err
	}
	b, err := resolver.Resolve(args.SourceB)
	if err != nil {
		return err
	}
	midpoint := (a + b) / 2
	if err := runtime.WriteWCSOffset(ctx, args.Destination.WCS, args.Destination.Axis, midpoint); err != nil {
		return err
	}
	verifiedOffsets, err := runtime.ReadWCSOffsets(ctx)
	if err != nil {
		return err
	}
	got, err := (WCSResolver{Offsets: verifiedOffsets}).Resolve(args.Destination)
	if err != nil {
		return err
	}
	if math.Abs(got-midpoint) > m100VerificationTolerance {
		return fmt.Errorf("M100 verification failed: %s %s = %.6f, want %.6f", args.Destination.WCS, args.Destination.Axis, got, midpoint)
	}
	return nil
}

func handleM101(ctx context.Context, runtime Runtime, inv Invocation) error {
	args, err := parseM101Args(inv.CleanArgs)
	if err != nil {
		return err
	}
	offsets, err := runtime.ReadWCSOffsets(ctx)
	if err != nil {
		return err
	}
	resolver := WCSResolver{Offsets: offsets}
	a, err := resolver.Resolve(args.First)
	if err != nil {
		return err
	}
	b, err := resolver.Resolve(args.Second)
	if err != nil {
		return err
	}
	if math.Abs(a-b)-args.Tolerance > 1e-12 {
		return fmt.Errorf("WCS comparison failed: %s %s=%.6f %s %s=%.6f tolerance=%.6f", args.First.WCS, args.First.Axis, a, args.Second.WCS, args.Second.Axis, b, args.Tolerance)
	}
	return nil
}

func handleM102(ctx context.Context, runtime Runtime, inv Invocation) error {
	parts := strings.SplitN(inv.RawArgs, "=", 2)
	if len(parts) != 2 {
		if strings.TrimSpace(inv.RawArgs) == "" {
			return fmt.Errorf("missing destination WCS axis")
		}
		return fmt.Errorf("missing expression")
	}
	destText := strings.TrimSpace(parts[0])
	if destText == "" {
		return fmt.Errorf("missing destination WCS axis")
	}
	dest, err := ParseWCSAxisRef(destText)
	if err != nil {
		return err
	}
	expr := strings.TrimSpace(parts[1])
	if expr == "" {
		return fmt.Errorf("missing expression")
	}
	evalCtx := EvalContext{Vars: runtime.Variables()}
	if expressionNeedsWCS(expr) {
		offsets, err := runtime.ReadWCSOffsets(ctx)
		if err != nil {
			return err
		}
		evalCtx.Offsets = offsets
	}
	value, err := EvalArithmeticExpression(expr, evalCtx)
	if err != nil {
		return err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("non-finite expression result")
	}
	return runtime.WriteWCSOffset(ctx, dest.WCS, dest.Axis, value)
}

func handleM106(ctx context.Context, runtime Runtime, inv Invocation) error {
	comparison, message := splitM106ErrorClause(inv.RawArgs)
	left, op, right, err := splitComparison(comparison)
	if err != nil {
		return err
	}
	evalCtx := EvalContext{Vars: runtime.Variables()}
	if expressionNeedsWCS(left) || expressionNeedsWCS(right) {
		offsets, err := runtime.ReadWCSOffsets(ctx)
		if err != nil {
			return err
		}
		evalCtx.Offsets = offsets
	}
	lv, err := EvalOperand(left, evalCtx)
	if err != nil {
		return err
	}
	rv, err := EvalOperand(right, evalCtx)
	if err != nil {
		return err
	}
	ok := false
	switch op {
	case "<":
		ok = lv < rv
	case "<=":
		ok = lv <= rv
	case ">":
		ok = lv > rv
	case ">=":
		ok = lv >= rv
	case "==":
		ok = lv == rv
	case "!=":
		ok = lv != rv
	default:
		return fmt.Errorf("unsupported comparison operator %q", op)
	}
	if ok {
		return nil
	}
	if strings.TrimSpace(message) != "" {
		return fmt.Errorf("%s", strings.TrimSpace(message))
	}
	return fmt.Errorf("assertion failed: %s=%.6f %s %s=%.6f", strings.TrimSpace(left), lv, op, strings.TrimSpace(right), rv)
}

func splitM106ErrorClause(args string) (string, string) {
	fields := strings.Fields(args)
	for _, f := range fields {
		if strings.EqualFold(f, "ERROR") {
			idx := strings.Index(args, f)
			return strings.TrimSpace(args[:idx]), strings.TrimSpace(args[idx+len(f):])
		}
	}
	return strings.TrimSpace(args), ""
}

func splitComparison(input string) (string, string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", "", fmt.Errorf("missing left operand")
	}
	ops := []string{"<=", ">=", "==", "!=", "<", ">"}
	best := -1
	bestOp := ""
	for _, op := range ops {
		if idx := strings.Index(input, op); idx >= 0 && (best < 0 || idx < best) {
			best = idx
			bestOp = op
		}
	}
	if best < 0 {
		return "", "", "", fmt.Errorf("missing comparison operator")
	}
	left := strings.TrimSpace(input[:best])
	right := strings.TrimSpace(input[best+len(bestOp):])
	if left == "" {
		return "", "", "", fmt.Errorf("missing left operand")
	}
	if right == "" {
		return "", "", "", fmt.Errorf("missing right operand")
	}
	return left, bestOp, right, nil
}

func handleM107(ctx context.Context, runtime Runtime, inv Invocation) error {
	name, valueText, err := splitNameAndRemainder(inv.CleanArgs)
	if err != nil {
		return err
	}
	if err := ValidateVariableName(name); err != nil {
		return err
	}
	if strings.TrimSpace(valueText) == "" {
		return fmt.Errorf("missing value for variable %q", name)
	}
	value, err := ParseFiniteFloat(valueText)
	if err != nil {
		ref, refErr := ParseWCSAxisRef(valueText)
		if refErr != nil {
			if looksLikeWCSAxisRef(valueText) {
				return refErr
			}
			return err
		}
		offsets, readErr := runtime.ReadWCSOffsets(ctx)
		if readErr != nil {
			return readErr
		}
		value, err = (WCSResolver{Offsets: offsets}).Resolve(ref)
		if err != nil {
			return err
		}
	}
	vars := runtime.Variables()
	if vars == nil {
		return fmt.Errorf("variable store is not available")
	}
	vars.Set(name, value)
	return nil
}

func handleM108(ctx context.Context, runtime Runtime, inv Invocation) error {
	name, destText, err := splitNameAndRemainder(inv.CleanArgs)
	if err != nil {
		return err
	}
	if err := ValidateVariableName(name); err != nil {
		return err
	}
	if strings.TrimSpace(destText) == "" {
		return fmt.Errorf("missing destination WCS axis")
	}
	ref, err := ParseWCSAxisRef(destText)
	if err != nil {
		return err
	}
	vars := runtime.Variables()
	if vars == nil {
		return fmt.Errorf("variable store is not available")
	}
	value, ok := vars.Get(name)
	if !ok {
		return fmt.Errorf("unknown variable %q", name)
	}
	return runtime.WriteWCSOffset(ctx, ref.WCS, ref.Axis, value)
}

func handleM109(ctx context.Context, runtime Runtime, inv Invocation) error {
	if strings.TrimSpace(inv.RawArgs) == "" {
		return fmt.Errorf("missing probe command")
	}
	point, err := runtime.RunProbe(ctx, inv.RawArgs)
	if err != nil {
		return err
	}
	contour := runtime.Contour()
	if contour == nil {
		return fmt.Errorf("contour state is not available")
	}
	return contour.AddPoint(point)
}

func handleM110(_ context.Context, runtime Runtime, inv Invocation) error {
	if strings.TrimSpace(inv.CleanArgs) != "" {
		return fmt.Errorf("unexpected arguments")
	}
	contour := runtime.Contour()
	if contour == nil {
		return fmt.Errorf("contour state is not available")
	}
	return contour.Enable()
}

func handleM111(_ context.Context, runtime Runtime, inv Invocation) error {
	if strings.TrimSpace(inv.CleanArgs) != "" {
		return fmt.Errorf("unexpected arguments")
	}
	contour := runtime.Contour()
	if contour == nil {
		return fmt.Errorf("contour state is not available")
	}
	contour.Disable()
	return nil
}

func handleM112(_ context.Context, runtime Runtime, inv Invocation) error {
	if strings.TrimSpace(inv.CleanArgs) != "" {
		return fmt.Errorf("unexpected arguments")
	}
	contour := runtime.Contour()
	if contour == nil {
		return fmt.Errorf("contour state is not available")
	}
	contour.Clear()
	return nil
}

func splitNameAndRemainder(args string) (string, string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", "", fmt.Errorf("missing variable name")
	}
	fields := strings.Fields(args)
	name := fields[0]
	idx := strings.Index(args, name) + len(name)
	return name, strings.TrimSpace(args[idx:]), nil
}

func looksLikeWCSAxisRef(input string) bool {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return false
	}
	first := fields[0]
	if strings.EqualFold(first, "WCS") {
		return true
	}
	return strings.HasPrefix(strings.ToUpper(first), "G")
}
