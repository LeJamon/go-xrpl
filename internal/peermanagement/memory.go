package peermanagement

import "errors"

// MemoryBudget accounts for retained payloads and wakes readers when capacity returns.
type MemoryBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
	Changed() <-chan struct{}
}

// SetMemoryBudgets attaches node-owned accounts before the overlay starts.
// Critical outbound reservations are separate from the general outbound account.
func (o *Overlay) SetMemoryBudgets(inbound, outbound, critical MemoryBudget) error {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if o.lifecycleState != overlayLifecycleNew {
		return errors.New("peermanagement: memory budgets must be set before Run")
	}
	o.inboundReadBudget.mu.Lock()
	defer o.inboundReadBudget.mu.Unlock()
	o.outboundBudget.mu.Lock()
	defer o.outboundBudget.mu.Unlock()
	if o.inboundReadBudget.used != 0 || o.outboundBudget.activeAccounts != 0 {
		return errors.New("peermanagement: memory budgets already have active owners")
	}
	o.inboundReadBudget.parent = inbound
	o.outboundBudget.parent = outbound
	o.outboundBudget.criticalParent = critical
	return nil
}
