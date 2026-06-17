package grbl

import (
	"math"
	"strconv"
	"strings"
)

type ProbeResult struct {
	Position [3]float64
	Success  bool
}

func ParseProbeResult(line string) (ProbeResult, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[PRB:") || !strings.HasSuffix(line, "]") {
		return ProbeResult{}, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(line, "[PRB:"), "]")
	coordsText, statusText, ok := strings.Cut(body, ":")
	if !ok || statusText == "" {
		return ProbeResult{}, false
	}
	coords := strings.Split(coordsText, ",")
	if len(coords) != 3 {
		return ProbeResult{}, false
	}
	var result ProbeResult
	for i, coord := range coords {
		value, err := strconv.ParseFloat(strings.TrimSpace(coord), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return ProbeResult{}, false
		}
		result.Position[i] = value
	}
	switch strings.TrimSpace(statusText) {
	case "1":
		result.Success = true
	case "0":
		result.Success = false
	default:
		return ProbeResult{}, false
	}
	return result, true
}
