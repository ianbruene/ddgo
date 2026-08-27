//go:build miqt

package ui

import (
	"context"
	"os"

	"github.com/ianbruene/ddgo/internal/app"
	qt "github.com/mappu/miqt/qt"
)

// Application owns the Qt lifetime, top-level windows, and the single UI-side
// subscription to controller events.
type Application struct {
	controller *app.Controller

	pollTimer  *qt.QTimer
	dispatcher viewDispatcher
	windows    windowManager
}

func newApplication(controller *app.Controller) *Application {
	a := &Application{controller: controller}
	a.windows.dispatcher = &a.dispatcher
	return a
}

// Run creates and runs the UI application.
func Run(controller *app.Controller) error {
	return newApplication(controller).Run()
}

func (a *Application) Run() error {
	qt.NewQApplication(os.Args)

	state, revision := a.controller.SnapshotWithRevision()
	a.dispatcher.state = state
	a.dispatcher.revision = revision
	mainWindow := newMainWindow(a.controller, func() { a.openBlankWindow() })
	a.windows.add(mainWindow)
	mainWindow.show()

	a.pollTimer = qt.NewQTimer()
	a.pollTimer.OnTimeout(func() { a.drainControllerEvents() })
	a.pollTimer.Start(50)

	go func() { _ = a.controller.RefreshPorts(context.Background()) }()
	qt.QApplication_Exec()
	return nil
}

func (a *Application) openBlankWindow() {
	window := newBlankWindow(func() { a.openBlankWindow() })
	a.windows.add(window)
	window.show()
}

func (a *Application) drainControllerEvents() {
	for {
		select {
		case event := <-a.controller.Events():
			a.dispatcher.dispatch(event)
		default:
			return
		}
	}
}
