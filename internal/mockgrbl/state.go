package mockgrbl

import "time"

type State string

const (
	StateIdle  State = "Idle"
	StateRun   State = "Run"
	StateJog   State = "Jog"
	StateHold  State = "Hold"
	StateAlarm State = "Alarm"
	StateHome  State = "Home"
	StateCheck State = "Check"
	StateSleep State = "Sleep"
)

type Clock interface{ Now() time.Time }
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

type ManualClock struct{ T time.Time }

func (c *ManualClock) Now() time.Time {
	if c.T.IsZero() {
		c.T = time.Unix(0, 0)
	}
	return c.T
}
func (c *ManualClock) Advance(d time.Duration) { c.T = c.Now().Add(d) }

type MoveKind string

const (
	MoveJog    MoveKind = "jog"
	MoveNormal MoveKind = "normal"
)

type Move struct {
	Original  string     `json:"original"`
	Kind      MoveKind   `json:"kind"`
	Start     [3]float64 `json:"start"`
	Target    [3]float64 `json:"target"`
	StartTime time.Time  `json:"start_time"`
	Duration  float64    `json:"duration_seconds"`
	Feed      float64    `json:"feed"`
	Line      int        `json:"line"`
}
type MoveSnapshot struct {
	*Move
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	Progress       float64 `json:"progress"`
}
type Snapshot struct {
	State              State         `json:"state"`
	MachinePosition    [3]float64    `json:"machine_position"`
	ActiveMove         *MoveSnapshot `json:"active_move,omitempty"`
	QueueCapacity      int           `json:"queue_capacity"`
	QueuedCommandCount int           `json:"queued_command_count"`
	QueuedCommands     []string      `json:"queued_commands"`
	FreePlannerBlocks  int           `json:"free_planner_blocks"`
	FreeRXBytes        int           `json:"free_rx_bytes"`
	LastCommand        string        `json:"last_command"`
	LastResponse       string        `json:"last_response"`
	LastErrorAlarm     string        `json:"last_error_alarm"`
	ProfileName        string        `json:"profile_name"`
	ProfileVersion     string        `json:"profile_version"`
}
type LogEntry struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}
