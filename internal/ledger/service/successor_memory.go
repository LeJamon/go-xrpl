package service

import "sync"

// Failed work retains its original inputs. A replacement shares its reservation
// until publication transfers ownership; aborting releases only the extra bytes.
type successorMemory struct {
	mu     sync.Mutex
	budget MemoryBudget
	bytes  uint64
	claims map[*uint64]struct{}
}

func (m *successorMemory) claim(bytes, capacity uint64) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bytes > capacity {
		return nil, ErrPreferredTransitionMemory
	}
	if bytes > m.bytes {
		if m.budget != nil && !m.budget.TryReserve(bytes-m.bytes) {
			return nil, ErrPreferredTransitionMemory
		}
		m.bytes = bytes
	}
	m.claims[&bytes] = struct{}{}
	return sync.OnceFunc(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.claims, &bytes)
		var remaining uint64
		for claim := range m.claims {
			remaining = max(remaining, *claim)
		}
		if m.budget != nil && remaining < m.bytes {
			m.budget.Release(m.bytes - remaining)
		}
		m.bytes = remaining
	}), nil
}

func (p *PreparedPreferredLedger) reserveMemory(bytes uint64) (func(), error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	s := p.service
	s.mu.RLock()
	defer s.mu.RUnlock()
	if j := p.unfinished; j != nil && s.speculativeSuccessor == j && j.finished {
		p.memory = j.memory
	}
	if p.memory == nil {
		p.memory = &successorMemory{
			budget: s.config.PersistenceMemory,
			claims: make(map[*uint64]struct{}),
		}
	}
	return p.memory.claim(bytes, s.preferredTransitionMemoryCapacity())
}
