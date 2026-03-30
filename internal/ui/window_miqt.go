//go:build miqt

package ui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"example.com/cncui/internal/app"
	"example.com/cncui/internal/grbl"
	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
	qt "github.com/mappu/miqt/qt"
)

type Window struct {
	controller *app.Controller

	window *qt.QMainWindow

	console      *qt.QPlainTextEdit
	commandEntry *qt.QLineEdit
	sendButton   *qt.QPushButton

	portCombo      *qt.QComboBox
	refreshButton  *qt.QPushButton
	connectButton  *qt.QPushButton
	stepCombo      *qt.QComboBox
	feedCombo      *qt.QComboBox
	jogXPButton    *qt.QPushButton
	jogXMButton    *qt.QPushButton
	jogYPButton    *qt.QPushButton
	jogYMButton    *qt.QPushButton
	jogZPButton    *qt.QPushButton
	jogZMButton    *qt.QPushButton
	unlockButton   *qt.QPushButton
	homeButton     *qt.QPushButton
	resetButton    *qt.QPushButton
	holdButton     *qt.QPushButton
	resumeButton   *qt.QPushButton
	statusButton   *qt.QPushButton
	connStatus     *qt.QLabel
	machineStatus  *qt.QLabel
	lastErrorLabel *qt.QLabel
	pollTimer      *qt.QTimer
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
	w.window.SetWindowTitle("CNC UI")
	w.window.Resize(1100, 700)

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

	w.sendButton = qt.NewQPushButton(nil)
	w.sendButton.SetText("Send")
	commandRow.Layout().AddWidget(w.sendButton.QWidget)

	right := qt.NewQWidget(nil)
	right.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
	central.Layout().AddWidget(right)

	connectionGroup := qt.NewQGroupBox(nil)
	connectionGroup.SetTitle("Connection")
	connectionGroup.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
	right.Layout().AddWidget(connectionGroup.QWidget)

	w.portCombo = qt.NewQComboBox(nil)
	connectionGroup.Layout().AddWidget(label("Port").QWidget)
	connectionGroup.Layout().AddWidget(w.portCombo.QWidget)

	w.refreshButton = qt.NewQPushButton(nil)
	w.refreshButton.SetText("Refresh Ports")
	connectionGroup.Layout().AddWidget(w.refreshButton.QWidget)

	w.connectButton = qt.NewQPushButton(nil)
	w.connectButton.SetText("Connect")
	connectionGroup.Layout().AddWidget(w.connectButton.QWidget)

	jogGroup := qt.NewQGroupBox(nil)
	jogGroup.SetTitle("Jog")
	jogGroup.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
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

	actionsGroup := qt.NewQGroupBox(nil)
	actionsGroup.SetTitle("Machine Actions")
	actionsGroup.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
	right.Layout().AddWidget(actionsGroup.QWidget)

	w.unlockButton = button("Unlock")
	w.homeButton = button("Home")
	w.resetButton = button("Soft Reset")
	w.holdButton = button("Hold")
	w.resumeButton = button("Resume")
	w.statusButton = button("Status")
	for _, btn := range []*qt.QPushButton{w.unlockButton, w.homeButton, w.resetButton, w.holdButton, w.resumeButton, w.statusButton} {
		actionsGroup.Layout().AddWidget(btn.QWidget)
	}

	statusGroup := qt.NewQGroupBox(nil)
	statusGroup.SetTitle("Status")
	statusGroup.SetLayout(qt.NewQVBoxLayout(nil).QLayout)
	right.Layout().AddWidget(statusGroup.QWidget)

	w.connStatus = qt.NewQLabel(nil)
	w.machineStatus = qt.NewQLabel(nil)
	w.lastErrorLabel = qt.NewQLabel(nil)
	statusGroup.Layout().AddWidget(w.connStatus.QWidget)
	statusGroup.Layout().AddWidget(w.machineStatus.QWidget)
	statusGroup.Layout().AddWidget(w.lastErrorLabel.QWidget)

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
	w.resumeButton.OnClicked(func() { w.action(grbl.ActionResume) })
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
	if state.LastError != "" {
		w.lastErrorLabel.SetText("Last error: " + state.LastError)
	} else {
		w.lastErrorLabel.SetText("Last error: none")
	}

	enabled := state.Connected
	w.sendButton.SetEnabled(enabled)
	w.commandEntry.SetEnabled(enabled)
	for _, btn := range []*qt.QPushButton{w.jogXPButton, w.jogXMButton, w.jogYPButton, w.jogYMButton, w.jogZPButton, w.jogZMButton, w.unlockButton, w.homeButton, w.resetButton, w.holdButton, w.resumeButton, w.statusButton} {
		btn.SetEnabled(enabled)
	}
}

func (w *Window) appendConsole(prefix string, text string) {
	w.console.AppendPlainText(fmt.Sprintf("[%s] %s", prefix, text))
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

func spacer() *qt.QWidget {
	return qt.NewQWidget(nil)
}
