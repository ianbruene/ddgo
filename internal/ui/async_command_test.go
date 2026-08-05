package ui

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAsyncCommandSubmitterReturnsBeforeCommandCompletesAndPostsResult(t *testing.T) {
	uiQueue := make(chan func(), 8)
	done := make(chan error, 1)
	block := make(chan struct{})
	s := NewAsyncCommandSubmitter(context.Background(), func(fn func()) { uiQueue <- fn }, func(err error) { done <- err })

	if ok := s.Submit(func(context.Context) error {
		<-block
		return nil
	}); !ok {
		t.Fatal("Submit() = false, want true")
	}
	if !s.Busy() {
		t.Fatal("Busy() = false while command is pending")
	}
	select {
	case <-done:
		t.Fatal("completion posted before blocked command completed")
	default:
	}

	uiProcessed := make(chan struct{}, 1)
	uiQueue <- func() { uiProcessed <- struct{}{} }
	(<-uiQueue)()
	select {
	case <-uiProcessed:
	case <-time.After(time.Second):
		t.Fatal("UI queue did not process unrelated callback while command was pending")
	}

	close(block)
	(<-uiQueue)()
	if err := <-done; err != nil {
		t.Fatalf("done err = %v, want nil", err)
	}
	if s.Busy() {
		t.Fatal("Busy() = true after completion callback")
	}
}

func TestAsyncCommandSubmitterRejectsDuplicateAndReportsErrorOnUIQueue(t *testing.T) {
	uiQueue := make(chan func(), 8)
	wantErr := errors.New("boom")
	done := make(chan error, 1)
	block := make(chan struct{})
	s := NewAsyncCommandSubmitter(context.Background(), func(fn func()) { uiQueue <- fn }, func(err error) { done <- err })

	if !s.Submit(func(context.Context) error { <-block; return wantErr }) {
		t.Fatal("first Submit() = false, want true")
	}
	if s.Submit(func(context.Context) error { return nil }) {
		t.Fatal("second Submit() = true while busy, want false")
	}
	close(block)
	(<-uiQueue)()
	if err := <-done; !errors.Is(err, wantErr) {
		t.Fatalf("done err = %v, want %v", err, wantErr)
	}
}
