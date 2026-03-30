//go:build serial

package ports

import (
	"context"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

func ListPorts(_ context.Context) ([]Info, error) {
	detailed, err := enumerator.GetDetailedPortsList()
	if err == nil && len(detailed) > 0 {
		ports := make([]Info, 0, len(detailed))
		for _, p := range detailed {
			ports = append(ports, Info{
				Name:         p.Name,
				IsUSB:        p.IsUSB,
				VID:          p.VID,
				PID:          p.PID,
				SerialNumber: p.SerialNumber,
			})
		}
		return ports, nil
	}

	names, err := serial.GetPortsList()
	if err != nil {
		return nil, err
	}
	ports := make([]Info, 0, len(names))
	for _, name := range names {
		ports = append(ports, Info{Name: name})
	}
	return ports, nil
}
