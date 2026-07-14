package mockgrbl

import "fmt"

type FirmwareProfile struct {
	Name                                                                                       string `json:"name"`
	Version                                                                                    string `json:"version"`
	LineEnding                                                                                 string `json:"line_ending"`
	StatusByte, CycleStartByte, FeedHoldByte, SoftResetByte, AlternateResetByte, JogCancelByte byte
	PlannerBlockCapacity                                                                       int    `json:"planner_block_capacity"`
	SerialRXCapacity                                                                           int    `json:"serial_rx_capacity"`
	StrictUnsupported                                                                          bool   `json:"strict_unsupported"`
	JogLimitErrorCode                                                                          int    `json:"jog_limit_error_code"`
	JogLimitMessage                                                                            string `json:"jog_limit_message"`
	InvalidJogErrorCode                                                                        int    `json:"invalid_jog_error_code"`
	InvalidJogMessage                                                                          string `json:"invalid_jog_message"`
	LineOverflowErrorCode                                                                      int    `json:"line_overflow_error_code"`
	LineOverflowMessage                                                                        string `json:"line_overflow_message"`
	BuildDate                                                                                  string `json:"build_date"`
	GGRevision                                                                                 string `json:"gg_revision"`
	PCBRevision                                                                                string `json:"pcb_revision"`
}

func DefaultFirmwareProfile() FirmwareProfile {
	return FirmwareProfile{Name: "GrblDD", Version: "1.1g", LineEnding: "\r\n", StatusByte: '?', CycleStartByte: '~', FeedHoldByte: '!', SoftResetByte: 0x18, AlternateResetByte: '|', JogCancelByte: 0x85, PlannerBlockCapacity: 15, SerialRXCapacity: 128, StrictUnsupported: true, JogLimitErrorCode: 15, JogLimitMessage: "jogLIM", InvalidJogErrorCode: 16, InvalidJogMessage: "jogINV", LineOverflowErrorCode: 14, LineOverflowMessage: "2long", BuildDate: "20240619", GGRevision: "3A", PCBRevision: "3A"}
}
func (p FirmwareProfile) StartupBanner() string {
	return fmt.Sprintf("\r\nGrbl %s [help:'$']%s", p.Version, p.LineEnding)
}
func (p FirmwareProfile) OK() string           { return "ok" + p.LineEnding }
func (p FirmwareProfile) Msg(s string) string  { return "[MSG:" + s + "]" + p.LineEnding }
func (p FirmwareProfile) Error(n int) string   { return fmt.Sprintf("error:%d%s", n, p.LineEnding) }
func (p FirmwareProfile) Echo(s string) string { return "[echo: " + s + "]" + p.LineEnding }
func (p FirmwareProfile) Alarm(n int) string   { return fmt.Sprintf("ALARM:%d%s", n, p.LineEnding) }

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

func (p FirmwareProfile) BuildInfo() string {
	return fmt.Sprintf("[grbl:%s GG:%s PCB:%s YMD:%s]", p.Version, p.GGRevision, p.PCBRevision, p.BuildDate)
}
