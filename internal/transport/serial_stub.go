//go:build !serial

package transport

import (
	"context"
	"errors"
)

var ErrSerialTransportNotBuilt = errors.New("serial transport not built; rebuild with -tags serial")

type SerialTransport struct {
	events chan Event
}

func NewSerialTransport() *SerialTransport {
	return &SerialTransport{events: make(chan Event, 256)}
}

func (t *SerialTransport) Open(_ context.Context, _ PortConfig) error {
	return ErrSerialTransportNotBuilt
}
func (t *SerialTransport) Close() error { return nil }
func (t *SerialTransport) Write(_ context.Context, _ Message) error {
	return ErrSerialTransportNotBuilt
}
func (t *SerialTransport) Events() <-chan Event { return t.events }
