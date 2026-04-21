package main

import (
	"errors"
	"testing"

	"example.com/cncui/internal/app"
)

func TestRun_UsesUIRunner(t *testing.T) {
	t.Parallel()

	called := false
	err := run(func(_ *app.Controller) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if !called {
		t.Fatal("ui runner was not called")
	}
}

func TestRun_PropagatesUIError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("ui failed")
	err := run(func(_ *app.Controller) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("run() error = %v, want %v", err, wantErr)
	}
}
