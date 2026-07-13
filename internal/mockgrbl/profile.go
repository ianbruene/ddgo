package mockgrbl

import "fmt"

type FirmwareProfile struct {
	Name                                                                                       string `json:"name"`
	Version                                                                                    string `json:"version"`
	LineEnding                                                                                 string `json:"line_ending"`
	StatusByte, CycleStartByte, FeedHoldByte, SoftResetByte, AlternateResetByte, JogCancelByte byte
	PlannerBlockCapacity                                                                       int  `json:"planner_block_capacity"`
	SerialRXCapacity                                                                           int  `json:"serial_rx_capacity"`
	StrictUnsupported                                                                          bool `json:"strict_unsupported"`
}

func DefaultFirmwareProfile() FirmwareProfile {
	return FirmwareProfile{Name: "GrblDD", Version: "1.1g", LineEnding: "\r\n", StatusByte: '?', CycleStartByte: '~', FeedHoldByte: '!', SoftResetByte: 0x18, AlternateResetByte: '|', JogCancelByte: 0x85, PlannerBlockCapacity: 15, SerialRXCapacity: 128, StrictUnsupported: true}
}
func (p FirmwareProfile) StartupBanner() string {
	return fmt.Sprintf("\r\nGrbl %s [help:'$']%s", p.Version, p.LineEnding)
}
func (p FirmwareProfile) OK() string          { return "ok" + p.LineEnding }
func (p FirmwareProfile) Msg(s string) string { return "[MSG:" + s + "]" + p.LineEnding }
func (p FirmwareProfile) Error(n int) string  { return fmt.Sprintf("error:%d%s", n, p.LineEnding) }
func (p FirmwareProfile) Alarm(n int) string  { return fmt.Sprintf("ALARM:%d%s", n, p.LineEnding) }

type MachineProfile struct {
	Name                                   string     `json:"name"`
	Min                                    [3]float64 `json:"min"`
	Max                                    [3]float64 `json:"max"`
	DefaultFeed                            float64    `json:"default_feed"`
	InitialPosition                        [3]float64 `json:"initial_position"`
	SoftLimits, HardLimits, HomingRequired bool
	PlannerQueueCapacity, SerialRXCapacity int
}

func DefaultMachineProfile() MachineProfile {
	return MachineProfile{Name: "GG3-ish", Min: [3]float64{-86.5, -241.5, -78.5}, Max: [3]float64{0, 0, 0}, DefaultFeed: 500, InitialPosition: [3]float64{0, 0, 0}, SoftLimits: true, HardLimits: true, PlannerQueueCapacity: 15, SerialRXCapacity: 128}
}
