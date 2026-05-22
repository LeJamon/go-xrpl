package resource

// Consumer is a handle to a tracked endpoint inside a Manager. Hold
// one per peer; call Charge as the peer does work. Release once when
// the peer is torn down so the Manager can age the entry out.
//
// Mirrors rippled's ripple::Resource::Consumer. Unlike the C++ type,
// the handle is not value-semantic — callers pass the *Consumer
// pointer and call Release exactly once. Charge is safe to call after
// Release (returns Ok) so race-y teardown paths don't panic.
type Consumer struct {
	m *Manager
	e *entry
}

func (c *Consumer) Endpoint() string {
	if c == nil || c.e == nil {
		return ""
	}
	return c.e.k.addr
}

func (c *Consumer) Kind() Kind {
	if c == nil || c.e == nil {
		return KindInbound
	}
	return c.e.k.kind
}

func (c *Consumer) IsUnlimited() bool {
	if c == nil || c.e == nil {
		return false
	}
	return c.e.isUnlimited()
}

// Charge applies fee and returns the resulting Disposition. Released
// or nil consumers return Ok. Unlimited consumers short-circuit at the
// Consumer boundary, matching rippled's Consumer::charge at
// Consumer.cpp:106-114.
func (c *Consumer) Charge(fee Charge, context string) Disposition {
	if c == nil || c.m == nil || c.e == nil {
		return Ok
	}
	if c.e.isUnlimited() {
		return Ok
	}
	return c.m.charge(c.e, fee, context)
}

// Disposition reports the current disposition without changing the balance.
func (c *Consumer) Disposition() Disposition {
	if c == nil || c.m == nil || c.e == nil {
		return Ok
	}
	return c.m.charge(c.e, Charge{cost: 0}, "")
}

func (c *Consumer) Warn() bool {
	if c == nil || c.m == nil || c.e == nil {
		return false
	}
	return c.m.warn(c.e)
}

// Disconnect applies a feeDrop penalty when the balance is over the
// drop threshold so an immediate reconnect from the same endpoint
// stays blacklisted; returns whether the caller should drop the
// connection now.
func (c *Consumer) Disconnect() bool {
	if c == nil || c.m == nil || c.e == nil {
		return false
	}
	return c.m.disconnect(c.e)
}

func (c *Consumer) Balance() int {
	if c == nil || c.m == nil || c.e == nil {
		return 0
	}
	return c.m.balance(c.e)
}

// Release is idempotent — only the first call decrements the
// Manager's refcount.
func (c *Consumer) Release() {
	if c == nil || c.m == nil || c.e == nil {
		return
	}
	c.m.release(c.e)
	c.e = nil
	c.m = nil
}
