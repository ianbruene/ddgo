//go:build !miqt

package ui

import (
	"errors"

	"example.com/cncui/internal/app"
)

var ErrMIQTNotBuilt = errors.New("MIQT UI not built; rebuild with -tags miqt")

func Run(_ *app.Controller) error {
	return ErrMIQTNotBuilt
}
