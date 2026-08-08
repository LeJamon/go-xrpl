package resource

import (
	"sync"
	"time"
)

type Completion struct {
	Disposition Disposition
	Warning     bool
	Balance     int64
}

type Admission struct {
	once     sync.Once
	manager  *Manager
	entry    *entry
	reserved int64
	result   Completion
}

func (m *Manager) AdmitInbound(addr string, reservation Charge) (*Admission, Disposition) {
	consumer := m.NewInboundEndpoint(addr)
	if consumer == nil {
		return nil, Drop
	}
	admission, result := consumer.Admit(reservation)
	consumer.Release()
	return admission, result
}

func (m *Manager) AdmitUnlimited(addr string) (*Admission, Disposition) {
	consumer := m.NewUnlimitedEndpoint(addr)
	if consumer == nil {
		return nil, Drop
	}
	admission, result := consumer.Admit(Charge{})
	consumer.Release()
	return admission, result
}

func (m *Manager) admit(e *entry, reservation Charge) (*Admission, Disposition) {
	m.mu.Lock()
	now := m.clock()
	if e.isUnlimited() {
		e.localRefs++
		m.cancelEntryExpiryLocked(e)
		m.mu.Unlock()
		return &Admission{manager: m, entry: e}, Ok
	}

	balance := e.balance(now)
	if balance >= DropThreshold {
		balance = e.add(int64(FeeDrop.Cost()), now)
		m.stats.drops++
		endpoint := e.k.addr
		m.mu.Unlock()
		m.journal.Warn("resource admission dropped", "endpoint", endpoint, "balance", balance, "threshold", DropThreshold)
		return nil, Drop
	}

	reserved := int64(reservation.Cost())
	projected := saturatingAdd(balance, saturatingAdd(e.reservedCost, reserved)/int64(DecayWindowSeconds))
	if e.inflight >= m.limits.MaxInflightPerConsumer || projected >= DropThreshold {
		m.stats.inflightRejections++
		m.stats.drops++
		m.mu.Unlock()
		return nil, Drop
	}

	e.inflight++
	m.inflight++
	e.reservedCost = saturatingAdd(e.reservedCost, reserved)
	e.localRefs++
	m.cancelEntryExpiryLocked(e)
	m.mu.Unlock()
	return &Admission{manager: m, entry: e, reserved: reserved}, disposition(projected)
}

func (a *Admission) Finish(actual Charge, context string) Completion {
	if a == nil || a.manager == nil || a.entry == nil {
		return Completion{Disposition: Ok}
	}
	a.once.Do(func() {
		a.result = a.manager.finishAdmission(a.entry, a.reserved, actual, context)
	})
	return a.result
}

func (a *Admission) Cancel() Completion {
	return a.Finish(Charge{}, "")
}

func (m *Manager) finishAdmission(e *entry, reserved int64, actual Charge, context string) Completion {
	m.mu.Lock()
	now := m.clock()
	result := Completion{Disposition: Ok}
	if !e.isUnlimited() {
		if e.inflight > 0 {
			e.inflight--
			m.inflight--
		}
		e.reservedCost -= reserved
		if e.reservedCost < 0 {
			e.reservedCost = 0
		}
		result.Balance = e.add(int64(actual.Cost()), now)
		result.Disposition = disposition(result.Balance)
		if result.Balance >= WarningThreshold && (!e.warningSet || now.Sub(e.lastWarning) >= time.Second) {
			result.Balance = e.add(int64(FeeWarning.Cost()), now)
			e.lastWarning = now
			e.warningSet = true
			result.Warning = true
			m.stats.warnings++
		}
	}
	endpoint := e.k.addr
	m.releaseLocalLocked(e, now)
	m.mu.Unlock()

	if actual.Cost() != 0 {
		if context == "" {
			m.journal.Debug("resource charge", "endpoint", endpoint, "fee", actual.String(), "balance", result.Balance)
		} else {
			m.journal.Debug("resource charge", "endpoint", endpoint, "fee", actual.String(), "balance", result.Balance, "context", context)
		}
	}
	if result.Warning {
		m.journal.Info("resource load warning", "endpoint", endpoint)
	}
	return result
}
