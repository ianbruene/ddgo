//go:build !miqt

package ui

import (
	"errors"
	"testing"

	"example.com/cncui/internal/app"
	"example.com/cncui/internal/ports"
	"example.com/cncui/internal/transport"
)

func TestRunStub(t *testing.T) {
	t.Parallel()

	controller := app.NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))
	if err := Run(controller); !errors.Is(err, ErrMIQTNotBuilt) {
		t.Fatalf("Run() error = %v, want %v", err, ErrMIQTNotBuilt)
	}
}
