package macro

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var variableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ParseFiniteFloat(input string) (float64, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return 0, fmt.Errorf("invalid numeric value %q", input)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value %q", input)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid numeric value %q", input)
	}
	return v, nil
}

func ValidateVariableName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("missing variable name")
	}
	if !variableNameRE.MatchString(name) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	return nil
}

type m100Args struct {
	SourceA     WCSAxisRef
	SourceB     WCSAxisRef
	Destination WCSAxisRef
}

type m101Args struct {
	First     WCSAxisRef
	Second    WCSAxisRef
	Tolerance float64
}

func parseM100Args(input string) (m100Args, error) {
	fields := strings.Fields(input)
	first, rest, err := consumeWCSAxisRef(fields)
	if err != nil {
		return m100Args{}, withMissingWCSAxisMessage(err, "missing first source WCS axis")
	}
	second, rest, err := consumeWCSAxisRef(rest)
	if err != nil {
		return m100Args{}, withMissingWCSAxisMessage(err, "missing second source WCS axis")
	}
	dest, rest, err := consumeWCSAxisRef(rest)
	if err != nil {
		return m100Args{}, withMissingWCSAxisMessage(err, "missing destination WCS axis")
	}
	if len(rest) > 0 {
		return m100Args{}, fmt.Errorf("unexpected arguments: %q", strings.Join(rest, " "))
	}
	return m100Args{SourceA: first, SourceB: second, Destination: dest}, nil
}

func parseM101Args(input string) (m101Args, error) {
	fields := strings.Fields(input)
	first, rest, err := consumeWCSAxisRef(fields)
	if err != nil {
		return m101Args{}, withMissingWCSAxisMessage(err, "missing first WCS axis")
	}
	second, rest, err := consumeWCSAxisRef(rest)
	if err != nil {
		return m101Args{}, withMissingWCSAxisMessage(err, "missing second WCS axis")
	}
	if len(rest) == 0 {
		return m101Args{}, fmt.Errorf("missing tolerance")
	}
	tolText := rest[0]
	tol, err := ParseFiniteFloat(tolText)
	if err != nil {
		return m101Args{}, fmt.Errorf("invalid tolerance %q", tolText)
	}
	if tol < 0 {
		return m101Args{}, fmt.Errorf("negative tolerance %q", tolText)
	}
	if len(rest) > 1 {
		return m101Args{}, fmt.Errorf("unexpected arguments: %q", strings.Join(rest[1:], " "))
	}
	return m101Args{First: first, Second: second, Tolerance: tol}, nil
}

func consumeWCSAxisRef(fields []string) (WCSAxisRef, []string, error) {
	if len(fields) == 0 {
		return WCSAxisRef{}, nil, fmt.Errorf("missing WCS axis reference")
	}
	max := 1
	if strings.EqualFold(fields[0], "WCS") {
		max = 3
	} else if compactWCSAxisRE.MatchString(fields[0]) {
		max = 1
	} else {
		max = 2
	}
	if max > len(fields) {
		max = len(fields)
	}
	var bestErr error
	for n := max; n >= 1; n-- {
		ref, err := ParseWCSAxisRef(strings.Join(fields[:n], " "))
		if err == nil {
			return ref, fields[n:], nil
		}
		if bestErr == nil || strings.Contains(err.Error(), "unsupported") {
			bestErr = err
		}
	}
	return WCSAxisRef{}, fields, bestErr
}

func withMissingWCSAxisMessage(err error, msg string) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), "missing") {
		return fmt.Errorf(msg)
	}
	return err
}
