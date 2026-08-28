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
const durableReadQueue = durableReadWorkers

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
	slots chan struct{}
	tasks chan durableReadTask
}

func (p *durableReadPool) start() {
	p.once.Do(func() {
		p.slots = make(chan struct{}, durableReadWorkers+durableReadQueue)
		p.tasks = make(chan durableReadTask, durableReadQueue)
		for range durableReadWorkers {
			go p.run()
		}
	})
}

func (p *durableReadPool) reserve(ctx context.Context) bool {
	p.start()
	select {
	case p.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *durableReadPool) release() {
	<-p.slots
}

func (p *durableReadPool) submit(task durableReadTask) {
	p.tasks <- task
}

func (p *durableReadPool) run() {
	for task := range p.tasks {
		task.coalescer.read(task.ctx, task.hash, task.flight, task.read)
		p.release()
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
	flight := c.flights[hash]
	c.mu.Unlock()
	if flight == nil {
		if !durableReads.reserve(ctx) {
			return nil, ctx.Err()
		}
		created := false
		c.mu.Lock()
		if c.flights == nil {
			c.flights = make(map[[32]byte]*durableReadFlight)
		}
		flight = c.flights[hash]
		if flight == nil {
			flight = &durableReadFlight{done: make(chan struct{})}
			c.flights[hash] = flight
			created = true
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
		} else {
			durableReads.release()
		}
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
