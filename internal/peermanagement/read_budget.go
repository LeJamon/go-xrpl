package peermanagement

import (
	"context"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

type readBudget struct {
	mu       sync.Mutex
	capacity int64
	used     int64
	wake     chan struct{}
}

func newReadBudget(capacity int64) *readBudget {
	return &readBudget{capacity: capacity, wake: make(chan struct{})}
}

func (b *readBudget) acquire(ctx context.Context, closeCh <-chan struct{}, size int64) error {
	if b == nil || size <= 0 {
		return nil
	}
	if size > b.capacity {
		return message.ErrMessageTooLarge
	}
	for {
		b.mu.Lock()
		if size <= b.capacity-b.used {
			b.used += size
			b.mu.Unlock()
			return nil
		}
		wake := b.wake
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closeCh:
			return ErrConnectionClosed
		case <-wake:
		}
	}
}

func (b *readBudget) release(size int64) {
	if b == nil || size <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= size
	wake := b.wake
	b.wake = make(chan struct{})
	close(wake)
	b.mu.Unlock()
}
