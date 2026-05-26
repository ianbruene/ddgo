package grbl

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ianbruene/ddgo/internal/transport"
)

type StatusReport struct {
	Raw     string
	State   string
	MPos    [3]float64
	HasMPos bool
	WPos    [3]float64
	HasWPos bool
	Feed    float64
	Spindle float64
	HasFS   bool
}

type Action string

const (
	ActionUnlock    Action = "unlock"
	ActionHome      Action = "home"
	ActionHold      Action = "hold"
	ActionResume    Action = "resume"
	ActionStatus    Action = "status"
	ActionSoftReset Action = "soft_reset"
)

func BuildJog(axis string, delta float64, feed float64) (transport.Message, error) {
	axis = strings.ToUpper(strings.TrimSpace(axis))
	if axis != "X" && axis != "Y" && axis != "Z" {
		return transport.Message{}, fmt.Errorf("unsupported jog axis %q", axis)
	}
	if math.Abs(delta) < 1e-12 {
		return transport.Message{}, errors.New("jog distance must be non-zero")
	}
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return transport.Message{}, errors.New("jog distance must be finite")
	}
	if feed <= 0 {
		return transport.Message{}, errors.New("jog feed must be greater than zero")
	}
	if math.IsNaN(feed) || math.IsInf(feed, 0) {
		return transport.Message{}, errors.New("jog feed must be finite")
	}
	line := fmt.Sprintf("$J=G91 %s%.3f F%.0f", axis, delta, feed)
	return transport.NewLineMessage(line), nil
}

func BuildAction(action Action) (transport.Message, error) {
	switch action {
	case ActionUnlock:
		return transport.NewLineMessage("$X"), nil
	case ActionHome:
		return transport.NewLineMessage("$H"), nil
	case ActionHold:
		return transport.NewRawMessage([]byte("!"), "!"), nil
	case ActionResume:
		return transport.NewRawMessage([]byte("~"), "~"), nil
	case ActionStatus:
		return transport.NewRawMessage([]byte("?"), "?"), nil
	case ActionSoftReset:
		return transport.NewRawMessage([]byte{0x18}, "Ctrl-X"), nil
	default:
		return transport.Message{}, fmt.Errorf("unsupported action %q", action)
	}
}

func ParseMachineState(line string) string {
	report, ok := ParseStatusReport(line)
	if !ok {
		return ""
	}
	return report.State
}

func ParseStatusReport(line string) (StatusReport, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "<") || !strings.HasSuffix(line, ">") {
		return StatusReport{}, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(line, "<"), ">")
	parts := strings.Split(body, "|")
	if len(parts) == 0 {
		return StatusReport{}, false
	}
	report := StatusReport{Raw: line, State: strings.TrimSpace(parts[0])}
	if report.State == "" {
		return StatusReport{}, false
	}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MPos":
			coords, ok := parseCoordTriple(value)
			if !ok {
				return StatusReport{}, false
			}
			report.MPos = coords
			report.HasMPos = true
		case "WPos":
			coords, ok := parseCoordTriple(value)
			if !ok {
				return StatusReport{}, false
			}
			report.WPos = coords
			report.HasWPos = true
		case "FS":
			feed, spindle, ok := parseFeedSpindle(value)
			if !ok {
				return StatusReport{}, false
			}
			report.Feed = feed
			report.Spindle = spindle
			report.HasFS = true
		}
	}
	return report, true
}

func parseCoordTriple(value string) ([3]float64, bool) {
	values := strings.Split(value, ",")
	if len(values) != 3 {
		return [3]float64{}, false
	}
	var coords [3]float64
	for i := range coords {
		v, err := strconv.ParseFloat(strings.TrimSpace(values[i]), 64)
		if err != nil {
			return [3]float64{}, false
		}
		coords[i] = v
	}
	return coords, true
}

func parseFeedSpindle(value string) (float64, float64, bool) {
	values := strings.Split(value, ",")
	if len(values) != 2 {
		return 0, 0, false
	}
	feed, err := strconv.ParseFloat(strings.TrimSpace(values[0]), 64)
	if err != nil {
		return 0, 0, false
	}
	spindle, err := strconv.ParseFloat(strings.TrimSpace(values[1]), 64)
	if err != nil {
		return 0, 0, false
	}
	return feed, spindle, true
}
