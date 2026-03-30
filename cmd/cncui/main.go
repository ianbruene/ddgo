package main

import (
	"fmt"
	"os"

	"example.com/cncui/internal/app"
	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
	"example.com/cncui/internal/ui"
)

func main() {
	controller := app.NewController(transport.NewSerialTransport(), ports.ListPorts)
	if err := ui.Run(controller); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
