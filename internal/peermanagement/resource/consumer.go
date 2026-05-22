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

// Endpoint returns the normalized address key used by the Manager.
func (c *Consumer) Endpoint() string {
	if c == nil || c.e == nil {
		return ""
	}
	return c.e.k.addr
}

// Kind returns the consumer's kind.
func (c *Consumer) Kind() Kind {
	if c == nil || c.e == nil {
		return KindInbound
	}
	return c.e.k.kind
}

// IsUnlimited reports whether the consumer is privileged.
func (c *Consumer) IsUnlimited() bool {
	if c == nil || c.e == nil {
		return false
	}
	return c.e.isUnlimited()
}

// Charge applies fee and returns the resulting Disposition. context is
// an optional short label that joins the diagnostic log line.
// Released or nil consumers return Ok.
func (c *Consumer) Charge(fee Charge, context string) Disposition {
	if c == nil || c.m == nil || c.e == nil {
		return Ok
	}
	return c.m.charge(c.e, fee, context)
}

// Disposition queries the current disposition without adding to the
// balance. Charges with cost zero are functionally equivalent; this
// helper avoids the syntactic noise.
func (c *Consumer) Disposition() Disposition {
	if c == nil || c.m == nil || c.e == nil {
		return Ok
	}
	return c.m.charge(c.e, Charge{cost: 0}, "")
}

// Warn issues a warning charge if the balance is over the warning
// threshold. Returns true if a warning was issued this call.
func (c *Consumer) Warn() bool {
	if c == nil || c.m == nil || c.e == nil {
		return false
	}
	return c.m.warn(c.e)
}

// Disconnect reports whether the consumer should be dropped now and,
// if so, applies a one-time feeDrop penalty so an immediate reconnect
// from the same endpoint stays blacklisted.
func (c *Consumer) Disconnect() bool {
	if c == nil || c.m == nil || c.e == nil {
		return false
	}
	return c.m.disconnect(c.e)
}

// Balance returns the consumer's current balance.
func (c *Consumer) Balance() int {
	if c == nil || c.m == nil || c.e == nil {
		return 0
	}
	return c.m.balance(c.e)
}

// Release drops this handle's reference. Safe to call multiple times;
// only the first call decrements the Manager's refcount.
func (c *Consumer) Release() {
	if c == nil || c.m == nil || c.e == nil {
		return
	}
	c.m.release(c.e)
	c.e = nil
	c.m = nil
}
