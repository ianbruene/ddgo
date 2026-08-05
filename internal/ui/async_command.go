package ui

import (
	"context"
	"sync"
)

// AsyncCommandSubmitter runs controller-backed UI commands outside the caller's
// event handler and reports completion through a caller-provided UI-safe queue.
type AsyncCommandSubmitter struct {
	mu     sync.Mutex
	busy   bool
	ctx    context.Context
	cancel context.CancelFunc
	post   func(func())
	onDone func(error)
}

func NewAsyncCommandSubmitter(parent context.Context, post func(func()), onDone func(error)) *AsyncCommandSubmitter {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if post == nil {
		post = func(fn func()) { fn() }
	}
	return &AsyncCommandSubmitter{ctx: ctx, cancel: cancel, post: post, onDone: onDone}
}

func (s *AsyncCommandSubmitter) Submit(run func(context.Context) error) bool {
	if run == nil {
		return false
	}
	s.mu.Lock()
	if s.busy || s.ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	s.busy = true
	ctx := s.ctx
	s.mu.Unlock()

	go func() {
		err := run(ctx)
		s.post(func() {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
			if s.onDone != nil {
				s.onDone(err)
			}
		})
	}()
	return true
}

func (s *AsyncCommandSubmitter) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *AsyncCommandSubmitter) Cancel() { s.cancel() }
