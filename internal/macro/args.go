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
