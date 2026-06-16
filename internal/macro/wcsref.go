package macro

import (
	"fmt"
	"regexp"
	"strings"
)

type WCSAxisRef struct {
	WCS  WCS
	Axis Axis
}

type WCSResolver struct {
	Offsets WCSOffsets
}

var compactWCSAxisRE = regexp.MustCompile(`(?i)^(G5[4-9])([A-Za-z])$`)

func ParseWCSAxisRef(input string) (WCSAxisRef, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return WCSAxisRef{}, fmt.Errorf("missing WCS axis reference")
	}
	fields := strings.Fields(s)
	if len(fields) > 0 && strings.EqualFold(fields[0], "WCS") {
		fields = fields[1:]
	}
	var wcsName, axisName string
	if len(fields) == 1 {
		m := compactWCSAxisRE.FindStringSubmatch(fields[0])
		if m == nil {
			if strings.HasPrefix(strings.ToUpper(fields[0]), "G") {
				if _, err := normalizeWCS(fields[0]); err != nil {
					return WCSAxisRef{}, err
				}
				return WCSAxisRef{}, fmt.Errorf("missing WCS axis")
			}
			return WCSAxisRef{}, fmt.Errorf("missing WCS")
		}
		wcsName, axisName = m[1], m[2]
	} else if len(fields) == 2 {
		wcsName, axisName = fields[0], fields[1]
	} else if len(fields) == 0 {
		return WCSAxisRef{}, fmt.Errorf("missing WCS")
	} else {
		return WCSAxisRef{}, fmt.Errorf("invalid WCS axis reference %q", input)
	}
	wcs, err := normalizeWCS(wcsName)
	if err != nil {
		return WCSAxisRef{}, err
	}
	axis, err := normalizeWCSAxis(axisName)
	if err != nil {
		return WCSAxisRef{}, err
	}
	return WCSAxisRef{WCS: wcs, Axis: axis}, nil
}

func normalizeWCS(name string) (WCS, error) {
	s := strings.ToUpper(strings.TrimSpace(name))
	switch s {
	case "G54", "G55", "G56", "G57", "G58", "G59":
		return WCS(s), nil
	}
	if s == "" {
		return "", fmt.Errorf("missing WCS")
	}
	return "", fmt.Errorf("unsupported WCS %q", s)
}

func normalizeWCSAxis(name string) (Axis, error) {
	s := strings.ToUpper(strings.TrimSpace(name))
	switch Axis(s) {
	case AxisX, AxisY, AxisZ:
		return Axis(s), nil
	}
	if s == "" {
		return "", fmt.Errorf("missing WCS axis")
	}
	return "", fmt.Errorf("unsupported WCS axis %q", s)
}

func (r WCSResolver) Resolve(ref WCSAxisRef) (float64, error) {
	p, ok := r.Offsets[ref.WCS]
	if !ok {
		return 0, fmt.Errorf("missing WCS offset %q", ref.WCS)
	}
	switch ref.Axis {
	case AxisX:
		return p.X, nil
	case AxisY:
		return p.Y, nil
	case AxisZ:
		return p.Z, nil
	}
	return 0, fmt.Errorf("unsupported WCS axis %q", ref.Axis)
}
