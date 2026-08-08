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
	finished bool
}

func newMessageCharge(peer *Peer, messageName string) *messageCharge {
	return &messageCharge{
		peer:    peer,
		fee:     resource.FeeTrivialPeer(),
		context: messageName,
	}
}

func (c *messageCharge) update(fee resource.Charge, chargeContext string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.finished || fee.Cost() < c.fee.Cost() {
		c.mu.Unlock()
		return
	}
	c.fee = fee
	if chargeContext != "" {
		c.context = strings.TrimSpace(c.context + " " + chargeContext)
	}
	c.mu.Unlock()
}

func (c *messageCharge) finish() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished = true
	peer := c.peer
	fee := c.fee
	chargeContext := c.context
	c.mu.Unlock()
	if peer != nil {
		peer.Charge(fee, chargeContext)
	}
}
