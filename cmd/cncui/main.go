package main

import (
	"fmt"
	"os"

	"example.com/cncui/internal/app"
	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
	"example.com/cncui/internal/ui"
)

func run(uiRunner func(*app.Controller) error) error {
	controller := app.NewController(transport.NewSerialTransport(), ports.ListPorts)
	return uiRunner(controller)
}

func main() {
	if err := run(ui.Run); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
