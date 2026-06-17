package macro

import (
	"strings"
	"testing"
)

func TestEvalArithmeticExpression(t *testing.T) {
	vars := NewVariableStore()
	vars.Set("depth", -2)
	ctx := EvalContext{Offsets: WCSOffsets{"G54": {Z: 3}, "G55": {X: 5}}, Vars: vars}
	tests := []struct {
		in   string
		want float64
	}{
		{"1", 1}, {"-2", -2}, {"1 + 2 - 3", 0}, {"2 * 3 / 4", 1.5},
		{"1 + 2 * 3", 7}, {"(1 + 2) * 3", 9}, {"G54Z + 0.125", 3.125}, {"depth / 2", -1},
	}
	for _, tt := range tests {
		got, err := EvalArithmeticExpression(tt.in, ctx)
		if err != nil || got != tt.want {
			t.Fatalf("%q = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
}

func TestEvalArithmeticExpressionErrors(t *testing.T) {
	ctx := EvalContext{Offsets: WCSOffsets{"G54": {Z: 3}}, Vars: NewVariableStore()}
	tests := []struct{ in, want string }{
		{"missing", "unknown variable"}, {"G55Z", "missing WCS offset"}, {"G60Z", "unsupported WCS"}, {"G54A", "unsupported WCS axis"},
		{"1 +", "incomplete expression"}, {"1 2", "unexpected token"}, {"1 / 0", "division by zero"}, {"1e309", "non-finite"},
	}
	for _, tt := range tests {
		_, err := EvalArithmeticExpression(tt.in, ctx)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%q error=%v, want %q", tt.in, err, tt.want)
		}
	}
}

func TestEvalArithmeticExpressionCompactWCSReference(t *testing.T) {
	ctx := EvalContext{Offsets: WCSOffsets{"G54": {Z: 1}}, Vars: NewVariableStore()}
	got, err := EvalArithmeticExpression("G54Z + 1", ctx)
	if err != nil {
		t.Fatalf("EvalArithmeticExpression compact WCS ref error = %v", err)
	}
	if got != 2 {
		t.Fatalf("EvalArithmeticExpression compact WCS ref = %v, want 2", got)
	}
}

func TestEvalArithmeticExpressionSpacedWCSReferenceRejected(t *testing.T) {
	ctx := EvalContext{Offsets: WCSOffsets{"G54": {Z: 1}}, Vars: NewVariableStore()}
	tests := []string{"G54 Z + 1"}
	for _, input := range tests {
		_, err := EvalArithmeticExpression(input, ctx)
		if err == nil {
			t.Fatalf("EvalArithmeticExpression(%q) spaced WCS ref error = nil", input)
		}
		if !strings.Contains(err.Error(), "missing WCS axis") && !strings.Contains(err.Error(), "unexpected token") {
			t.Fatalf("EvalArithmeticExpression(%q) error = %v, want missing WCS axis or unexpected token", input, err)
		}
	}
}
