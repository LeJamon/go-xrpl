package peermanagement

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

type inboundReservation struct {
	budget *readBudget
	size   int64
	refs   atomic.Int64
}

func newInboundReservation(budget *readBudget, size int64) *inboundReservation {
	if budget == nil || size <= 0 {
		return nil
	}
	r := &inboundReservation{budget: budget, size: size}
	r.refs.Store(1)
	return r
}

func (r *inboundReservation) retain() *inboundReservation {
	if r == nil {
		return nil
	}
	for {
		refs := r.refs.Load()
		if refs <= 0 {
			return nil
		}
		if r.refs.CompareAndSwap(refs, refs+1) {
			return r
		}
	}
}

func (r *inboundReservation) release() {
	if r == nil {
		return
	}
	if r.refs.Add(-1) == 0 {
		r.budget.release(r.size)
	}
}

type readBudget struct {
	mu       sync.Mutex
	capacity int64
	used     int64
	wake     chan struct{}
	parent   MemoryBudget
}

func newReadBudget(capacity int64) *readBudget {
	return &readBudget{capacity: capacity}
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
		var parentWake <-chan struct{}
		if size <= b.capacity-b.used {
			admitted := b.parent == nil || b.parent.TryReserve(uint64(size))
			if !admitted {
				parentWake = b.parent.Changed()
				admitted = b.parent.TryReserve(uint64(size))
			}
			if admitted {
				b.used += size
				b.mu.Unlock()
				return nil
			}
		}
		if b.wake == nil {
			b.wake = make(chan struct{})
		}
		wake := b.wake
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closeCh:
			return ErrConnectionClosed
		case <-wake:
		case <-parentWake:
		}
	}
}

func (b *readBudget) release(size int64) {
	if b == nil || size <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= size
	if b.used < 0 {
		b.mu.Unlock()
		panic("peermanagement: inbound read budget released below zero")
	}
	if b.parent != nil {
		b.parent.Release(uint64(size))
	}
	if b.wake != nil {
		close(b.wake)
		b.wake = nil
	}
	b.mu.Unlock()
}
