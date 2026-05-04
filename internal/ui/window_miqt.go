//go:build miqt

package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ianbruene/ddgo/internal/app"
	"github.com/ianbruene/ddgo/internal/grbl"
	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
	qt "github.com/mappu/miqt/qt"
)

type Window struct {
	controller *app.Controller

	window *qt.QMainWindow

	console      *qt.QPlainTextEdit
	commandEntry *qt.QLineEdit
	sendButton   *qt.QPushButton

	portCombo       *qt.QComboBox
	refreshButton   *qt.QPushButton
	connectButton   *qt.QPushButton
	programPath     *qt.QLineEdit
	browseButton    *qt.QPushButton
	runButton       *qt.QPushButton
	pauseButton     *qt.QPushButton
	resumeButton    *qt.QPushButton
	stopButton      *qt.QPushButton
	stepCombo       *qt.QComboBox
	feedCombo       *qt.QComboBox
	jogXPButton     *qt.QPushButton
	jogXMButton     *qt.QPushButton
	jogYPButton     *qt.QPushButton
	jogYMButton     *qt.QPushButton
	jogZPButton     *qt.QPushButton
	jogZMButton     *qt.QPushButton
	unlockButton    *qt.QPushButton
	homeButton      *qt.QPushButton
	resetButton     *qt.QPushButton
	holdButton      *qt.QPushButton
	resumeActBtn    *qt.QPushButton
	statusButton    *qt.QPushButton
	connStatus      *qt.QLabel
	machineStatus   *qt.QLabel
	programStatus   *qt.QLabel
	programProgress *qt.QLabel
	lastErrorLabel  *qt.QLabel
	pollTimer       *qt.QTimer
}

func Run(controller *app.Controller) error {
	qt.NewQApplication(os.Args)
	w := newWindow(controller)
	w.window.Show()
	go func() { _ = controller.RefreshPorts(context.Background()) }()
	qt.QApplication_Exec()
	return nil
}

func newWindow(controller *app.Controller) *Window {
	w := &Window{controller: controller}
	w.build()
	w.bind()
	w.applyState(controller.Snapshot())
	return w
}

func (w *Window) build() {
	w.window = qt.NewQMainWindow(nil)
	w.window.SetWindowTitle("DDGo")
	w.window.Resize(1180, 760)

	central := qt.NewQWidget(nil)
	central.SetLayout(qt.NewQHBoxLayout(nil).QLayout)
	w.window.SetCentralWidget(central)

	left := qt.NewQWidget(nil)
	left.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
	central.Layout().AddWidget(left)

	w.console = qt.NewQPlainTextEdit(nil)
	w.console.SetReadOnly(true)
	left.Layout().AddWidget(w.console.QWidget)

	commandRow := qt.NewQWidget(nil)
	commandRow.SetLayout(qt.NewQHBoxLayout(nil).QLayout)
	left.Layout().AddWidget(commandRow)

	w.commandEntry = qt.NewQLineEdit(nil)
	w.commandEntry.SetPlaceholderText("Enter GRBL command, e.g. G0 X10")
	commandRow.Layout().AddWidget(w.commandEntry.QWidget)

	w.sendButton = button("Send")
	commandRow.Layout().AddWidget(w.sendButton.QWidget)

	right := qt.NewQWidget(nil)
	right.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
	central.Layout().AddWidget(right)

	connectionGroup := groupBox("Connection")
	right.Layout().AddWidget(connectionGroup.QWidget)
	w.portCombo = qt.NewQComboBox(nil)
	connectionGroup.Layout().AddWidget(label("Port").QWidget)
	connectionGroup.Layout().AddWidget(w.portCombo.QWidget)
	w.refreshButton = button("Refresh Ports")
	connectionGroup.Layout().AddWidget(w.refreshButton.QWidget)
	w.connectButton = button("Connect")
	connectionGroup.Layout().AddWidget(w.connectButton.QWidget)

	programGroup := groupBox("Program")
	right.Layout().AddWidget(programGroup.QWidget)
	programGroup.Layout().AddWidget(label("G-code file").QWidget)
	pathRow := qt.NewQWidget(nil)
	pathRow.SetLayout(qt.NewQHBoxLayout(nil).QLayout)
	programGroup.Layout().AddWidget(pathRow)
	w.programPath = qt.NewQLineEdit(nil)
	w.programPath.SetPlaceholderText("No program selected")
	w.programPath.SetReadOnly(true)
	pathRow.Layout().AddWidget(w.programPath.QWidget)
	w.browseButton = button("Open…")
	pathRow.Layout().AddWidget(w.browseButton.QWidget)
	w.runButton = button("Run")
	programGroup.Layout().AddWidget(w.runButton.QWidget)
	w.pauseButton = button("Pause")
	programGroup.Layout().AddWidget(w.pauseButton.QWidget)
	w.resumeButton = button("Resume")
	programGroup.Layout().AddWidget(w.resumeButton.QWidget)
	w.stopButton = button("Stop")
	programGroup.Layout().AddWidget(w.stopButton.QWidget)

	jogGroup := groupBox("Jog")
	right.Layout().AddWidget(jogGroup.QWidget)

	rowY := qt.NewQWidget(nil)
	rowY.SetLayout(qt.NewQHBoxLayout(nil).QLayout)
	jogGroup.Layout().AddWidget(rowY)
	rowY.Layout().AddWidget(spacer())
	w.jogYPButton = button("Y+")
	rowY.Layout().AddWidget(w.jogYPButton.QWidget)
	rowY.Layout().AddWidget(spacer())

	rowXY := qt.NewQWidget(nil)
	rowXY.SetLayout(qt.NewQHBoxLayout(nil).QLayout)
	jogGroup.Layout().AddWidget(rowXY)
	w.jogXMButton = button("X-")
	w.jogYMButton = button("Y-")
	w.jogXPButton = button("X+")
	rowXY.Layout().AddWidget(w.jogXMButton.QWidget)
	rowXY.Layout().AddWidget(w.jogYMButton.QWidget)
	rowXY.Layout().AddWidget(w.jogXPButton.QWidget)

	rowZ := qt.NewQWidget(nil)
	rowZ.SetLayout(qt.NewQHBoxLayout(nil).QLayout)
	jogGroup.Layout().AddWidget(rowZ)
	w.jogZMButton = button("Z-")
	w.jogZPButton = button("Z+")
	rowZ.Layout().AddWidget(w.jogZMButton.QWidget)
	rowZ.Layout().AddWidget(w.jogZPButton.QWidget)

	jogGroup.Layout().AddWidget(label("Step").QWidget)
	w.stepCombo = qt.NewQComboBox(nil)
	w.stepCombo.AddItems([]string{"0.01", "0.10", "1.00", "10.00"})
	w.stepCombo.SetCurrentText("0.10")
	jogGroup.Layout().AddWidget(w.stepCombo.QWidget)

	jogGroup.Layout().AddWidget(label("Feed").QWidget)
	w.feedCombo = qt.NewQComboBox(nil)
	w.feedCombo.AddItems([]string{"100", "250", "500", "1000", "2000"})
	w.feedCombo.SetCurrentText("500")
	jogGroup.Layout().AddWidget(w.feedCombo.QWidget)

	actionsGroup := groupBox("Machine Actions")
	right.Layout().AddWidget(actionsGroup.QWidget)
	w.unlockButton = button("Unlock")
	w.homeButton = button("Home")
	w.resetButton = button("Soft Reset")
	w.holdButton = button("Hold")
	w.resumeActBtn = button("Resume")
	w.statusButton = button("Status")
	for _, btn := range []*qt.QPushButton{w.unlockButton, w.homeButton, w.resetButton, w.holdButton, w.resumeActBtn, w.statusButton} {
		actionsGroup.Layout().AddWidget(btn.QWidget)
	}

	statusGroup := groupBox("Status")
	right.Layout().AddWidget(statusGroup.QWidget)
	w.connStatus = qt.NewQLabel(nil)
	w.machineStatus = qt.NewQLabel(nil)
	w.programStatus = qt.NewQLabel(nil)
	w.programProgress = qt.NewQLabel(nil)
	w.lastErrorLabel = qt.NewQLabel(nil)
	for _, lbl := range []*qt.QLabel{w.connStatus, w.machineStatus, w.programStatus, w.programProgress, w.lastErrorLabel} {
		statusGroup.Layout().AddWidget(lbl.QWidget)
	}

	w.pollTimer = qt.NewQTimer()
	w.pollTimer.OnTimeout(func() { w.drainEvents() })
	w.pollTimer.Start(50)
}

func (w *Window) bind() {
	w.sendButton.OnClicked(func() { w.sendCommand() })
	w.commandEntry.OnReturnPressed(func() { w.sendCommand() })
	w.refreshButton.OnClicked(func() {
		go func() { _ = w.controller.RefreshPorts(context.Background()) }()
	})
	w.connectButton.OnClicked(func() { w.toggleConnection() })
	w.browseButton.OnClicked(func() { w.browseAndLoadProgram() })
	w.runButton.OnClicked(func() { w.startProgram() })
	w.pauseButton.OnClicked(func() { go func() { _ = w.controller.PauseProgram(context.Background()) }() })
	w.resumeButton.OnClicked(func() { go func() { _ = w.controller.ResumeProgram(context.Background()) }() })
	w.stopButton.OnClicked(func() { go func() { _ = w.controller.StopProgram(context.Background()) }() })
	w.jogXPButton.OnClicked(func() { w.jog("X", +1) })
	w.jogXMButton.OnClicked(func() { w.jog("X", -1) })
	w.jogYPButton.OnClicked(func() { w.jog("Y", +1) })
	w.jogYMButton.OnClicked(func() { w.jog("Y", -1) })
	w.jogZPButton.OnClicked(func() { w.jog("Z", +1) })
	w.jogZMButton.OnClicked(func() { w.jog("Z", -1) })
	w.unlockButton.OnClicked(func() { w.action(grbl.ActionUnlock) })
	w.homeButton.OnClicked(func() { w.action(grbl.ActionHome) })
	w.resetButton.OnClicked(func() { w.action(grbl.ActionSoftReset) })
	w.holdButton.OnClicked(func() { w.action(grbl.ActionHold) })
	w.resumeActBtn.OnClicked(func() { w.action(grbl.ActionResume) })
	w.statusButton.OnClicked(func() { w.action(grbl.ActionStatus) })
}

func (w *Window) toggleConnection() {
	state := w.controller.Snapshot()
	if state.Connected {
		go func() { _ = w.controller.Disconnect() }()
		return
	}
	cfg := transport.DefaultPortConfig(strings.TrimSpace(w.portCombo.CurrentText()))
	go func() { _ = w.controller.Connect(context.Background(), cfg) }()
}

func (w *Window) browseAndLoadProgram() {
	dialog := qt.NewQFileDialog(w.window.QWidget)
	dialog.SetWindowTitle("Open G-code Program")
	dialog.SetFileMode(qt.QFileDialog__ExistingFile)
	dialog.SetNameFilter("G-code Files (*.gcode *.gc *.nc *.tap *.ngc);;All Files (*)")
	dialog.Exec()
	files := dialog.SelectedFiles()
	if len(files) == 0 {
		return
	}
	path := filepath.Clean(strings.TrimSpace(files[0]))
	if path == "" {
		return
	}
	w.programPath.SetText(path)
	go func() { _ = w.controller.LoadProgramFile(path) }()
}

func (w *Window) startProgram() {
	go func() { _ = w.controller.StartProgram(context.Background()) }()
}

func (w *Window) sendCommand() {
	line := strings.TrimSpace(w.commandEntry.Text())
	if line == "" {
		return
	}
	w.commandEntry.SetText("")
	go func() { _ = w.controller.SendConsoleLine(context.Background(), line) }()
}

func (w *Window) jog(axis string, direction float64) {
	step, err := strconv.ParseFloat(strings.TrimSpace(w.stepCombo.CurrentText()), 64)
	if err != nil {
		w.appendConsole("ERR", fmt.Sprintf("invalid step: %v", err))
		return
	}
	feed, err := strconv.ParseFloat(strings.TrimSpace(w.feedCombo.CurrentText()), 64)
	if err != nil {
		w.appendConsole("ERR", fmt.Sprintf("invalid feed: %v", err))
		return
	}
	go func() { _ = w.controller.Jog(context.Background(), axis, step*direction, feed) }()
}

func (w *Window) action(action grbl.Action) {
	go func() { _ = w.controller.Action(context.Background(), action) }()
}

func (w *Window) drainEvents() {
	for {
		select {
		case ev := <-w.controller.Events():
			w.applyEvent(ev)
		default:
			return
		}
	}
}

func (w *Window) applyEvent(ev app.Event) {
	w.applyState(ev.State)
	switch ev.Kind {
	case app.EventConsoleTX:
		w.appendConsole("TX", ev.Text)
	case app.EventConsoleRX:
		w.appendConsole("RX", ev.Text)
	case app.EventError:
		w.appendConsole("ERR", ev.Text)
	case app.EventPortsRefreshed:
		w.populatePorts(ev.Ports)
	case app.EventStateChanged:
		if ev.Text != "" {
			w.appendConsole("SYS", ev.Text)
		}
	}
}

func (w *Window) populatePorts(list []ports.Info) {
	w.portCombo.Clear()
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.Name)
	}
	if len(names) > 0 {
		w.portCombo.AddItems(names)
		w.portCombo.SetCurrentText(names[0])
	}
}

func (w *Window) applyState(state app.State) {
	if state.Connected {
		w.connStatus.SetText(fmt.Sprintf("Connection: connected (%s)", state.PortName))
		w.connectButton.SetText("Disconnect")
	} else {
		w.connStatus.SetText("Connection: disconnected")
		w.connectButton.SetText("Connect")
	}
	machine := state.MachineState
	if machine == "" {
		machine = "unknown"
	}
	w.machineStatus.SetText("Machine: " + machine)
	if state.ProgramPath != "" && strings.TrimSpace(w.programPath.Text()) == "" {
		w.programPath.SetText(state.ProgramPath)
	}
	w.programStatus.SetText("Program: " + formatProgramStatus(state))
	w.programProgress.SetText(fmt.Sprintf("Progress: %d / %d", state.ProgramComplete, state.ProgramTotal))
	if state.LastError != "" {
		w.lastErrorLabel.SetText("Last error: " + state.LastError)
	} else {
		w.lastErrorLabel.SetText("Last error: none")
	}

	programActive := state.ProgramStatus.IsActive()
	connected := state.Connected
	loaded := state.ProgramTotal > 0
	canManual := connected && !programActive
	canRun := connected && loaded && !programActive

	w.refreshButton.SetEnabled(!programActive)
	w.connectButton.SetEnabled(!programActive)
	w.portCombo.SetEnabled(!programActive)
	w.programPath.SetEnabled(!programActive)
	w.browseButton.SetEnabled(!programActive)
	w.runButton.SetEnabled(canRun)
	w.pauseButton.SetEnabled(state.ProgramStatus == app.ProgramRunning)
	w.resumeButton.SetEnabled(state.ProgramStatus == app.ProgramPaused)
	w.stopButton.SetEnabled(programActive)

	w.sendButton.SetEnabled(canManual)
	w.commandEntry.SetEnabled(canManual)
	w.stepCombo.SetEnabled(canManual)
	w.feedCombo.SetEnabled(canManual)
	for _, btn := range []*qt.QPushButton{w.jogXPButton, w.jogXMButton, w.jogYPButton, w.jogYMButton, w.jogZPButton, w.jogZMButton, w.unlockButton, w.homeButton, w.resetButton, w.holdButton, w.resumeActBtn, w.statusButton} {
		btn.SetEnabled(canManual)
	}
}

func (w *Window) appendConsole(prefix string, text string) {
	w.console.AppendPlainText(fmt.Sprintf("[%s] %s", prefix, text))
}

func formatProgramStatus(state app.State) string {
	switch state.ProgramStatus {
	case app.ProgramNotLoaded:
		return "not loaded"
	case app.ProgramLoaded:
		return fmt.Sprintf("loaded (%s)", state.ProgramName)
	case app.ProgramRunning:
		return fmt.Sprintf("running (%s)", state.ProgramName)
	case app.ProgramPaused:
		return fmt.Sprintf("paused (%s)", state.ProgramName)
	case app.ProgramStopped:
		return fmt.Sprintf("stopped (%s)", state.ProgramName)
	case app.ProgramCompleted:
		return fmt.Sprintf("completed (%s)", state.ProgramName)
	case app.ProgramFailed:
		return fmt.Sprintf("failed (%s)", state.ProgramName)
	default:
		return string(state.ProgramStatus)
	}
}

func label(text string) *qt.QLabel {
	l := qt.NewQLabel(nil)
	l.SetText(text)
	return l
}

func button(text string) *qt.QPushButton {
	b := qt.NewQPushButton(nil)
	b.SetText(text)
	return b
}

func groupBox(title string) *qt.QGroupBox {
	g := qt.NewQGroupBox(nil)
	g.SetTitle(title)
	g.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
	return g
}

func spacer() *qt.QWidget {
	return qt.NewQWidget(nil)
}
