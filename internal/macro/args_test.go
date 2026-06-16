package macro

import (
	"math"
	"strings"
	"testing"
)

func TestValidateVariableName(t *testing.T) {
	valid := []string{"depth", "_tmp", "foo1", "Foo_Bar"}
	for _, name := range valid {
		if err := ValidateVariableName(name); err != nil {
			t.Fatalf("ValidateVariableName(%q) error = %v", name, err)
		}
	}
	invalid := []string{"", "1foo", "foo-bar", "foo.bar", "foo bar"}
	for _, name := range invalid {
		if err := ValidateVariableName(name); err == nil {
			t.Fatalf("ValidateVariableName(%q) error = nil", name)
		}
	}
}

func TestParseFiniteFloat(t *testing.T) {
	for _, input := range []string{"1", "-2.5", ".125"} {
		if _, err := ParseFiniteFloat(input); err != nil {
			t.Fatalf("ParseFiniteFloat(%q) error = %v", input, err)
		}
	}
	for _, input := range []string{"", "abc", "NaN", "+Inf", "-Inf"} {
		if v, err := ParseFiniteFloat(input); err == nil || math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("ParseFiniteFloat(%q) = %v, %v; want error", input, v, err)
		}
	}
}

func TestParseWCSAxisRef(t *testing.T) {
	tests := []struct {
		in   string
		want WCSAxisRef
	}{
		{"WCS G54 X", WCSAxisRef{WCS: "G54", Axis: AxisX}},
		{"G55 y", WCSAxisRef{WCS: "G55", Axis: AxisY}},
		{"g56z", WCSAxisRef{WCS: "G56", Axis: AxisZ}},
	}
	for _, tt := range tests {
		got, err := ParseWCSAxisRef(tt.in)
		if err != nil || got != tt.want {
			t.Fatalf("ParseWCSAxisRef(%q) = %#v, %v", tt.in, got, err)
		}
	}
	for _, input := range []string{"", "WCS", "G54", "G60 X", "G54 A", "G54 X Y"} {
		if _, err := ParseWCSAxisRef(input); err == nil {
			t.Fatalf("ParseWCSAxisRef(%q) error = nil", input)
		}
	}
}

func TestWCSResolver(t *testing.T) {
	r := WCSResolver{Offsets: WCSOffsets{"G54": {X: 1, Y: 2, Z: 3}}}
	got, err := r.Resolve(WCSAxisRef{WCS: "G54", Axis: AxisY})
	if err != nil || got != 2 {
		t.Fatalf("Resolve() = %v, %v", got, err)
	}
	if _, err := r.Resolve(WCSAxisRef{WCS: "G55", Axis: AxisX}); err == nil || !strings.Contains(err.Error(), "missing WCS offset") {
		t.Fatalf("missing Resolve error = %v", err)
	}
	if _, err := r.Resolve(WCSAxisRef{WCS: "G54", Axis: Axis("A")}); err == nil {
		t.Fatal("unsupported axis error = nil")
	}
}
