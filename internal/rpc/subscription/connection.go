package subscription

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

const MaxConsecutiveDrops = 8

type Connection struct {
	id               string
	sendChannel      chan []byte
	ctx              context.Context
	cancel           context.CancelFunc
	disconnect       func()
	encodeOutbound   func([]byte) []byte
	sendObserver     func(queued bool)
	consecutiveDrops atomic.Int32
	totalDrops       atomic.Uint64
	disconnects      atomic.Uint64
	apiVersion       atomic.Int32
	sendMu           sync.Mutex
	terminal         bool
	canceled         bool
}

type ConnectionStats struct {
	ConsecutiveDrops int32
	Drops            uint64
	Disconnects      uint64
	Terminal         bool
}

type sendOutcome struct {
	queued                 bool
	dropped                bool
	disconnectedTransition bool
}

func NewConnection(id string, sendChannel chan []byte) *Connection {
	return NewConnectionWithContext(context.Background(), id, sendChannel)
}

func NewConnectionWithContext(parent context.Context, id string, sendChannel chan []byte) *Connection {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent) //nolint:gosec // stored on Connection and invoked by Cancel
	return &Connection{id: id, sendChannel: sendChannel, ctx: ctx, cancel: cancel}
}

func (c *Connection) ID() string {
	if c == nil {
		return ""
	}
	return c.id
}

func (c *Connection) Context() context.Context {
	if c == nil || c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

func (c *Connection) Done() <-chan struct{} {
	return c.Context().Done()
}

func (c *Connection) Cancel() {
	if c == nil {
		return
	}
	c.sendMu.Lock()
	c.terminal = true
	if !c.canceled {
		c.canceled = true
		if c.cancel != nil {
			c.cancel()
		}
	}
	c.sendMu.Unlock()
}

func (c *Connection) Outbound() <-chan []byte {
	if c == nil {
		return nil
	}
	return c.sendChannel
}

func (c *Connection) SetDisconnect(callback func()) {
	c.sendMu.Lock()
	c.disconnect = callback
	c.sendMu.Unlock()
}

func (c *Connection) SetEncodeOutbound(encode func([]byte) []byte) {
	c.sendMu.Lock()
	c.encodeOutbound = encode
	c.sendMu.Unlock()
}

func (c *Connection) SetSendObserver(observer func(bool)) {
	c.sendMu.Lock()
	c.sendObserver = observer
	c.sendMu.Unlock()
}

func (c *Connection) SetAPIVersion(version int) {
	if version == 0 {
		version = types.DefaultApiVersion
	}
	c.apiVersion.Store(int32(version))
}

func (c *Connection) APIVersion() int {
	version := int(c.apiVersion.Load())
	if version == 0 {
		return types.DefaultApiVersion
	}
	return version
}

func (c *Connection) TrySend(data []byte) bool {
	return c.trySend(data).queued
}

func (c *Connection) trySend(data []byte) sendOutcome {
	if c == nil || c.sendChannel == nil {
		return sendOutcome{}
	}
	c.sendMu.Lock()
	if c.terminal || c.ctx.Err() != nil {
		c.terminal = true
		c.sendMu.Unlock()
		return sendOutcome{}
	}
	if c.encodeOutbound != nil {
		data = c.encodeOutbound(data)
	}
	if c.ctx.Err() != nil {
		c.terminal = true
		c.sendMu.Unlock()
		return sendOutcome{}
	}
	observer := c.sendObserver
	select {
	case c.sendChannel <- data:
		c.consecutiveDrops.Store(0)
		c.sendMu.Unlock()
		if observer != nil {
			observer(true)
		}
		return sendOutcome{queued: true}
	default:
		drops := c.consecutiveDrops.Add(1)
		c.totalDrops.Add(1)
		disconnect := drops >= MaxConsecutiveDrops
		if disconnect {
			c.terminal = true
			c.disconnects.Add(1)
		}
		disconnectCallback := c.disconnect
		c.sendMu.Unlock()
		if observer != nil {
			observer(false)
		}
		if disconnect && disconnectCallback != nil {
			disconnectCallback()
		}
		return sendOutcome{dropped: true, disconnectedTransition: disconnect}
	}
}

func (c *Connection) Stats() ConnectionStats {
	if c == nil {
		return ConnectionStats{}
	}
	c.sendMu.Lock()
	terminal := c.terminal
	c.sendMu.Unlock()
	return ConnectionStats{
		ConsecutiveDrops: c.consecutiveDrops.Load(),
		Drops:            c.totalDrops.Load(),
		Disconnects:      c.disconnects.Load(),
		Terminal:         terminal,
	}
}
