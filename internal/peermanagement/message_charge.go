package peermanagement

import (
	"strings"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

type messageCharge struct {
	mu       sync.Mutex
	peer     *Peer
	fee      resource.Charge
	context  string
	refs     int
	finished bool
}

func newMessageCharge(peer *Peer, messageName string) *messageCharge {
	return &messageCharge{
		peer:    peer,
		fee:     resource.FeeTrivialPeer(),
		context: messageName,
		refs:    1,
	}
}

func (c *messageCharge) retain() *messageCharge {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.refs == 0 {
		return nil
	}
	c.refs++
	return c
}

func (c *messageCharge) update(fee resource.Charge, chargeContext string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.finished || fee.Cost() <= c.fee.Cost() {
		c.mu.Unlock()
		return
	}
	c.fee = fee
	if chargeContext != "" {
		c.context = strings.TrimSpace(c.context + " " + chargeContext)
	}
	c.mu.Unlock()
}

func (c *messageCharge) charge(fee resource.Charge, chargeContext string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	if c.finished || c.peer == nil {
		c.mu.Unlock()
		return false
	}
	peer := c.peer
	c.mu.Unlock()
	peer.Charge(fee, chargeContext)
	return true
}

func (c *messageCharge) finish() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.finished || c.refs == 0 {
		c.mu.Unlock()
		return
	}
	if c.refs > 1 {
		c.refs--
		c.mu.Unlock()
		return
	}
	c.refs = 0
	c.finished = true
	peer := c.peer
	fee := c.fee
	chargeContext := c.context
	c.mu.Unlock()
	if peer != nil {
		peer.Charge(fee, chargeContext)
	}
}
