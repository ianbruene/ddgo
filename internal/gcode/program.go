package gcode

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Line struct {
	Number int
	Text   string
}

type Program struct {
	Path  string
	Name  string
	Lines []Line
}

func LoadFile(path string) (Program, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Program{}, errors.New("program path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Program{}, fmt.Errorf("read program: %w", err)
	}
	lines, err := Parse(string(data))
	if err != nil {
		return Program{}, err
	}
	return Program{
		Path:  path,
		Name:  filepath.Base(path),
		Lines: lines,
	}, nil
}

func Parse(src string) ([]Line, error) {
	scanner := bufio.NewScanner(strings.NewReader(src))
	out := make([]Line, 0, 128)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := sanitizeLine(scanner.Text())
		if line == "" {
			continue
		}
		out = append(out, Line{Number: lineNo, Text: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan program: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("program contains no runnable gcode lines")
	}
	return out, nil
}

func sanitizeLine(line string) string {
	line = strings.TrimPrefix(line, "\ufeff")
	line = stripParenComments(line)
	if idx := strings.IndexByte(line, ';'); idx >= 0 {
		line = line[:idx]
	}
	return strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
}

func stripParenComments(line string) string {
	var b strings.Builder
	depth := 0
	for _, r := range line {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
				continue
			}
			b.WriteRune(r)
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
