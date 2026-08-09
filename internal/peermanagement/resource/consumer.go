package resource

import "sync"

type consumerState struct {
	mu           sync.RWMutex
	m            *Manager
	e            *entry
	disconnected bool
}

type Consumer struct {
	state *consumerState
}

func (c *Consumer) current() (*Manager, *entry) {
	if c == nil || c.state == nil {
		return nil, nil
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	return c.state.m, c.state.e
}

func (c *Consumer) Endpoint() string {
	_, e := c.current()
	if e == nil {
		return ""
	}
	return e.k.addr
}

func (c *Consumer) Kind() Kind {
	_, e := c.current()
	if e == nil {
		return KindInbound
	}
	return e.k.kind
}

func (c *Consumer) IsUnlimited() bool {
	_, e := c.current()
	return e != nil && e.isUnlimited()
}

func (c *Consumer) Charge(fee Charge, context string) Disposition {
	if c == nil || c.state == nil {
		return Ok
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	if c.state.m == nil || c.state.e == nil {
		return Ok
	}
	return c.state.m.charge(c.state.e, fee, context)
}

func (c *Consumer) Admit(reservation Charge) (*Admission, Disposition) {
	if c == nil || c.state == nil {
		return nil, Ok
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	if c.state.m == nil || c.state.e == nil {
		return nil, Ok
	}
	return c.state.m.admit(c.state.e, reservation)
}

func (c *Consumer) Disposition() Disposition {
	if c == nil || c.state == nil {
		return Ok
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	if c.state.m == nil || c.state.e == nil {
		return Ok
	}
	return disposition(c.state.m.balance(c.state.e))
}

func (c *Consumer) Warn() bool {
	if c == nil || c.state == nil {
		return false
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	return c.state.m != nil && c.state.e != nil && c.state.m.warn(c.state.e)
}

func (c *Consumer) Disconnect() bool {
	if c == nil || c.state == nil {
		return false
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.m == nil || c.state.e == nil {
		return false
	}
	if c.state.disconnected {
		return true
	}
	if !c.state.m.disconnect(c.state.e) {
		return false
	}
	c.state.disconnected = true
	return true
}

func (c *Consumer) Balance() int64 {
	if c == nil || c.state == nil {
		return 0
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	if c.state.m == nil || c.state.e == nil {
		return 0
	}
	return c.state.m.balance(c.state.e)
}

func (c *Consumer) SetPublicKey(publicKey string) {
	if c == nil || c.state == nil || publicKey == "" {
		return
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	if c.state.m != nil && c.state.e != nil {
		c.state.m.setPublicKey(c.state.e, publicKey)
	}
}

func (c *Consumer) Release() {
	if c == nil || c.state == nil {
		return
	}
	c.state.mu.Lock()
	m, e := c.state.m, c.state.e
	c.state.m = nil
	c.state.e = nil
	c.state.mu.Unlock()
	if m != nil && e != nil {
		m.release(e)
	}
}
