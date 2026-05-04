package grbl

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ianbruene/ddgo/internal/transport"
)

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
	if feed <= 0 {
		return transport.Message{}, errors.New("jog feed must be greater than zero")
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
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "<") || !strings.HasSuffix(line, ">") {
		return ""
	}
	line = strings.TrimSuffix(strings.TrimPrefix(line, "<"), ">")
	parts := strings.SplitN(line, "|", 2)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}
