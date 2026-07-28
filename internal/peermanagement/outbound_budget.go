package peermanagement

import "sync"

type outboundBudget struct {
	mu sync.Mutex

	maxBytes            int64
	generalLimit        int64
	retainedBytes       int64
	generalBytes        int64
	maxReservedAccounts int
	reservedAccounts    int
	activeAccounts      int
}

type outboundBudgetAccount struct {
	budget *outboundBudget

	criticalBytes    int64
	nonCriticalBytes int64
	hasReserve       bool
	attached         bool
}

func newOutboundBudget(maxBytes int64, maxPeers int) *outboundBudget {
	return &outboundBudget{
		maxBytes:            maxBytes,
		generalLimit:        maxBytes - int64(maxPeers)*int64(outboundCriticalByteReserve),
		maxReservedAccounts: maxPeers,
	}
}

func (b *outboundBudget) attach() *outboundBudgetAccount {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	hasReserve := b.reservedAccounts < b.maxReservedAccounts
	if hasReserve {
		b.reservedAccounts++
	}
	b.activeAccounts++
	return &outboundBudgetAccount{budget: b, hasReserve: hasReserve, attached: true}
}

func (a *outboundBudgetAccount) reserve(bytes int64, critical bool) bool {
	if a == nil || bytes == 0 {
		return true
	}
	b := a.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if !a.attached || bytes < 0 {
		return false
	}
	if bytes > b.maxBytes-b.retainedBytes {
		return false
	}

	generalDelta := bytes
	if critical && a.hasReserve {
		oldOverflow := max(int64(0), a.criticalBytes-int64(outboundCriticalByteReserve))
		newOverflow := max(int64(0), a.criticalBytes+bytes-int64(outboundCriticalByteReserve))
		generalDelta = newOverflow - oldOverflow
	}
	if generalDelta > b.generalLimit-b.generalBytes {
		return false
	}

	b.retainedBytes += bytes
	b.generalBytes += generalDelta
	if critical {
		a.criticalBytes += bytes
	} else {
		a.nonCriticalBytes += bytes
	}
	return true
}

func (a *outboundBudgetAccount) release(bytes int64, critical bool) {
	if a == nil || bytes == 0 {
		return
	}
	b := a.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if !a.attached {
		return
	}
	a.releaseLocked(bytes, critical)
}

func (a *outboundBudgetAccount) releaseLocked(bytes int64, critical bool) {
	b := a.budget
	if critical && a.hasReserve {
		oldOverflow := max(int64(0), a.criticalBytes-int64(outboundCriticalByteReserve))
		a.criticalBytes -= bytes
		newOverflow := max(int64(0), a.criticalBytes-int64(outboundCriticalByteReserve))
		b.generalBytes -= oldOverflow - newOverflow
	} else if critical {
		a.criticalBytes -= bytes
		b.generalBytes -= bytes
	} else {
		a.nonCriticalBytes -= bytes
		b.generalBytes -= bytes
	}
	b.retainedBytes -= bytes
}

func (a *outboundBudgetAccount) close() {
	if a == nil {
		return
	}
	b := a.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if !a.attached {
		return
	}
	a.releaseLocked(a.criticalBytes, true)
	a.releaseLocked(a.nonCriticalBytes, false)
	a.attached = false
	if a.hasReserve {
		b.reservedAccounts--
	}
	b.activeAccounts--
}

func (b *outboundBudget) snapshot() (retained, general int64) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.retainedBytes, b.generalBytes
}
