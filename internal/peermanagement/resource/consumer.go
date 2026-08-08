package resource

import "sync"

type Consumer struct {
	mu sync.RWMutex
	m  *Manager
	e  *entry
}

func (c *Consumer) Endpoint() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.m == nil || c.e == nil {
		return ""
	}
	return c.e.k.addr
}

func (c *Consumer) Kind() Kind {
	if c == nil {
		return KindInbound
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.m == nil || c.e == nil {
		return KindInbound
	}
	return c.e.k.kind
}

func (c *Consumer) IsUnlimited() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m != nil && c.e != nil && c.e.isUnlimited()
}

func (c *Consumer) Charge(fee Charge, context string) Disposition {
	if c == nil {
		return Ok
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.m == nil || c.e == nil {
		return Ok
	}
	return c.m.charge(c.e, fee, context)
}

func (c *Consumer) Admit(reservation Charge) (*Admission, Disposition) {
	if c == nil {
		return nil, Ok
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.m == nil || c.e == nil {
		return nil, Ok
	}
	return c.m.admit(c.e, reservation)
}

func (c *Consumer) Disposition() Disposition {
	if c == nil {
		return Ok
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.m == nil || c.e == nil {
		return Ok
	}
	return disposition(c.m.balance(c.e))
}

func (c *Consumer) Warn() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m != nil && c.e != nil && c.m.warn(c.e)
}

func (c *Consumer) Disconnect() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m != nil && c.e != nil && c.m.disconnect(c.e)
}

func (c *Consumer) Balance() int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.m == nil || c.e == nil {
		return 0
	}
	return c.m.balance(c.e)
}

func (c *Consumer) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	m, e := c.m, c.e
	c.m = nil
	c.e = nil
	c.mu.Unlock()
	if m != nil && e != nil {
		m.release(e)
	}
}
