//go:build miqt

package ui

import (
	"github.com/ianbruene/ddgo/internal/app"
	qt "github.com/mappu/miqt/qt"
)

// BlankWindow is temporary top-level lifecycle scaffolding for future window
// types. It intentionally presents no application state yet.
type BlankWindow struct {
	window *qt.QMainWindow

	onClose       func()
	onNewWindow   func()
	closeNotified bool
}

func newBlankWindow(onNewWindow func()) *BlankWindow {
	w := &BlankWindow{
		window:      qt.NewQMainWindow(nil),
		onNewWindow: onNewWindow,
	}
	w.window.SetWindowTitle("DDGo")
	w.window.Resize(800, 600)
	w.window.SetAttribute(qt.WA_DeleteOnClose)

	fileMenu := w.window.MenuBar().AddMenuWithTitle("File")
	newWindowAction := fileMenu.AddAction("New Window temp")
	newWindowAction.OnTriggered(func() {
		if w.onNewWindow != nil {
			w.onNewWindow()
		}
	})
	w.window.OnCloseEvent(func(super func(*qt.QCloseEvent), event *qt.QCloseEvent) {
		super(event)
		if event.IsAccepted() && !w.closeNotified {
			w.closeNotified = true
			if w.onClose != nil {
				w.onClose()
			}
		}
	})
	return w
}

func (w *BlankWindow) show() { w.window.Show() }

func (w *BlankWindow) setOnClose(fn func()) { w.onClose = fn }

func (w *BlankWindow) applyState(app.State) {}

func (w *BlankWindow) applyEvent(app.Event) {}
