//go:build !serial

package ports

import (
	"context"
	"errors"
)

var ErrSerialSupportNotBuilt = errors.New("serial support not built; rebuild with -tags serial")

func ListPorts(_ context.Context) ([]Info, error) {
	return nil, ErrSerialSupportNotBuilt
}
