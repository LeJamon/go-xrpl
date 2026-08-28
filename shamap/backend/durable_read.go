package backend

import (
	"context"
	"sync"
)

type durableReadFlight struct {
	done chan struct{}
	data []byte
	err  error
}

type durableReadCoalescer struct {
	mu      sync.Mutex
	flights map[[32]byte]*durableReadFlight
}

const durableReadWorkers = 32

type durableReadTask struct {
	coalescer *durableReadCoalescer
	ctx       context.Context
	hash      [32]byte
	flight    *durableReadFlight
	read      func(context.Context, [32]byte) ([]byte, error)
}

// durableReads is shared by every NodeStore. Backed SHAMap walks already fan
// out over 16 root branches; a bounded pool preserves that I/O parallelism
// without creating another goroutine for every Pebble point lookup.
var durableReads durableReadPool

type durableReadPool struct {
	once  sync.Once
	mu    sync.Mutex
	ready *sync.Cond
	queue []durableReadTask
	head  int
}

func (p *durableReadPool) submit(task durableReadTask) {
	p.once.Do(func() {
		p.ready = sync.NewCond(&p.mu)
		for range durableReadWorkers {
			go p.run()
		}
	})
	p.mu.Lock()
	p.queue = append(p.queue, task)
	p.ready.Signal()
	p.mu.Unlock()
}

func (p *durableReadPool) run() {
	for {
		p.mu.Lock()
		for p.head == len(p.queue) {
			p.ready.Wait()
		}
		task := p.queue[p.head]
		p.queue[p.head] = durableReadTask{}
		p.head++
		if p.head >= 1024 && p.head*2 >= len(p.queue) {
			p.queue = append(p.queue[:0], p.queue[p.head:]...)
			p.head = 0
		}
		p.mu.Unlock()

		task.coalescer.read(task.ctx, task.hash, task.flight, task.read)
	}
}

func (c *durableReadCoalescer) fetch(
	ctx context.Context,
	hash [32]byte,
	read func(context.Context, [32]byte) ([]byte, error),
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.flights == nil {
		c.flights = make(map[[32]byte]*durableReadFlight)
	}
	flight := c.flights[hash]
	created := flight == nil
	if flight == nil {
		flight = &durableReadFlight{done: make(chan struct{})}
		c.flights[hash] = flight
	}
	c.mu.Unlock()
	if created {
		durableReads.submit(durableReadTask{
			coalescer: c,
			ctx:       context.WithoutCancel(ctx),
			hash:      hash,
			flight:    flight,
			read:      read,
		})
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.data, flight.err
	}
}

func (c *durableReadCoalescer) read(
	ctx context.Context,
	hash [32]byte,
	flight *durableReadFlight,
	read func(context.Context, [32]byte) ([]byte, error),
) {
	data, err := read(ctx, hash)

	c.mu.Lock()
	flight.data = data
	flight.err = err
	if c.flights[hash] == flight {
		delete(c.flights, hash)
	}
	close(flight.done)
	c.mu.Unlock()
}
