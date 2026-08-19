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

	mainWindow *MainWindow
	pollTimer  *qt.QTimer
	views      viewRegistry
}

func newApplication(controller *app.Controller) *Application {
	return &Application{controller: controller}
}

// Run creates and runs the UI application.
func Run(controller *app.Controller) error {
	return newApplication(controller).Run()
}

func (a *Application) Run() error {
	qt.NewQApplication(os.Args)

	a.mainWindow = newMainWindow(a.controller)
	a.registerView(a.mainWindow)
	a.mainWindow.window.Show()

	a.pollTimer = qt.NewQTimer()
	a.pollTimer.OnTimeout(func() { a.drainControllerEvents() })
	a.pollTimer.Start(50)

	go func() { _ = a.controller.RefreshPorts(context.Background()) }()
	qt.QApplication_Exec()
	return nil
}

func (a *Application) registerView(view eventView) viewID {
	id := a.views.add(view)
	view.applyState(a.controller.Snapshot())
	return id
}

func (a *Application) unregisterView(id viewID) {
	a.views.remove(id)
}

func (a *Application) drainControllerEvents() {
	for {
		select {
		case event := <-a.controller.Events():
			a.views.applyEvent(event)
		default:
			return
		}
	}
}
