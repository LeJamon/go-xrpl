package shamap

import (
	"context"
	"sync"
)

type durableReadFlight struct {
	done    chan struct{}
	data    []byte
	err     error
	waiters int
}

type durableReadCoalescer struct {
	mu      sync.Mutex
	flights map[[32]byte]*durableReadFlight
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
	if flight == nil {
		flight = &durableReadFlight{done: make(chan struct{})}
		c.flights[hash] = flight
		go c.read(context.WithoutCancel(ctx), hash, flight, read)
	}
	flight.waiters++
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.leave(flight)
		return nil, ctx.Err()
	case <-flight.done:
		c.leave(flight)
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

func (c *durableReadCoalescer) leave(flight *durableReadFlight) {
	c.mu.Lock()
	flight.waiters--
	c.mu.Unlock()
}
