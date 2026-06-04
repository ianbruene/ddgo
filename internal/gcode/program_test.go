package gcode

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	src := "\ufeff(heading)\n; full line comment\nG21\nG90 ; abs\nG0 X1 Y2 (move)\nM3 (spindle) S1000\n\n"
	got, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []Line{{Number: 3, Raw: "G21", Text: "G21"}, {Number: 4, Raw: "G90 ; abs", Text: "G90"}, {Number: 5, Raw: "G0 X1 Y2 (move)", Text: "G0 X1 Y2"}, {Number: 6, Raw: "M3 (spindle) S1000", Text: "M3 S1000"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParsePreservesRawAndCleanedCompatibility(t *testing.T) {
	t.Parallel()

	src := "  G1   X1   Y2   (move); trailing comment  \nM999X1Y2 ; macro comment\n"
	got, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got[0].Raw != "G1   X1   Y2   (move); trailing comment" {
		t.Fatalf("Raw = %q", got[0].Raw)
	}
	if got[0].Text != "G1 X1 Y2" {
		t.Fatalf("Text = %q, want sanitized output", got[0].Text)
	}
	if got[1].Raw != "M999X1Y2 ; macro comment" {
		t.Fatalf("Raw = %q", got[1].Raw)
	}
	if got[1].Text != "M999X1Y2" {
		t.Fatalf("Text = %q, want compact sanitized output", got[1].Text)
	}
}

func TestParseRawPreservesParenthesesSemicolonsAndLineNumbers(t *testing.T) {
	t.Parallel()

	src := "\n(comment only)\nG90 (absolute)\n; comment only\nG0 X1 ; semicolon comment\n"
	got, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []Line{{Number: 3, Raw: "G90 (absolute)", Text: "G90"}, {Number: 5, Raw: "G0 X1 ; semicolon comment", Text: "G0 X1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParse_EmptyProgram(t *testing.T) {
	t.Parallel()

	if _, err := Parse("(comment only)\n; another\n\n"); err == nil {
		t.Fatal("Parse() error = nil, want non-nil")
	}
}

func TestLoadFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "demo.gcode")
	if err := os.WriteFile(path, []byte("G0 X1\nG0 Y2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	prog, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := prog.Name, "demo.gcode"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := len(prog.Lines), 2; got != want {
		t.Fatalf("len(Lines) = %d, want %d", got, want)
	}
}
