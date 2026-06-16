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
	registry.Register(107, HandlerFunc(handleM107))
	registry.Register(108, HandlerFunc(handleM108))
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
