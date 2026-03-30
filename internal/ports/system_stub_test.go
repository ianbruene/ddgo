//go:build !serial

package ports

import (
	"context"
	"errors"
	"testing"
)

func TestListPortsStub(t *testing.T) {
	t.Parallel()

	_, err := ListPorts(context.Background())
	if !errors.Is(err, ErrSerialSupportNotBuilt) {
		t.Fatalf("ListPorts() error = %v, want %v", err, ErrSerialSupportNotBuilt)
	}
}
