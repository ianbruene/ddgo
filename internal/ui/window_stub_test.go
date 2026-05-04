//go:build !miqt

package ui

import (
	"errors"
	"testing"

	"github.com/ianbruene/ddgo/internal/app"
	"github.com/ianbruene/ddgo/internal/ports"
	"github.com/ianbruene/ddgo/internal/transport"
)

func TestRunStub(t *testing.T) {
	t.Parallel()

	controller := app.NewController(transport.NewFakeTransport(), ports.StaticList(nil, nil))
	if err := Run(controller); !errors.Is(err, ErrMIQTNotBuilt) {
		t.Fatalf("Run() error = %v, want %v", err, ErrMIQTNotBuilt)
	}
}
