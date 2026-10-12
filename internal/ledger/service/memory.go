package service

import (
	"context"
	"sync"
)

// MemoryBudget accounts for buffers retained by the optional relay cache.
type MemoryBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
}

// Covers retained map buckets, order capacity and compaction overlap at the entry cap.
const relayCacheStructureBytes = relayTxCacheMaxEntries * 512

func (s *Service) reserveRelayCacheLocked(bytes uint64) bool {
	budget := s.config.RelayMemory
	if budget == nil {
		return true
	}
	if !s.relayTxCacheStructureReserved {
		if !budget.TryReserve(relayCacheStructureBytes) {
			return false
		}
		s.relayTxCacheStructureReserved = true
	}
	for !budget.TryReserve(bytes) {
		if s.relayTxCacheHead == len(s.relayTxCacheOrder) {
			s.compactRelayCacheOrderLocked()
			return false
		}
		old := s.relayTxCacheOrder[s.relayTxCacheHead]
		s.relayTxCacheHead++
		if record, ok := s.relayTxCache[old.hash]; ok && record.orderID == old.id {
			delete(s.relayTxCache, old.hash)
			s.relayTxCacheBytes -= int64(len(record.blob))
			budget.Release(uint64(len(record.blob)))
		}
	}
	return true
}

func (s *Service) releaseRelayCachePayloadLocked(bytes uint64) {
	if s.config.RelayMemory != nil {
		s.config.RelayMemory.Release(bytes)
	}
}

func (s *Service) clearRelayCache() {
	s.relayTxCacheMu.Lock()
	defer s.relayTxCacheMu.Unlock()
	bytes := s.relayTxCacheBytes
	s.relayTxCache = nil
	s.relayTxCacheOrder = nil
	s.relayTxCacheBytes = 0
	s.releaseRelayCachePayloadLocked(uint64(bytes))
	s.compactRelayCacheOrderLocked()
}

const persistenceAdmissionLimit = 8

func (s *Service) acquireLedgerWork(ctx context.Context) (func(), error) {
	return s.acquireLedgerWorkAdmission(ctx, true)
}

func (s *Service) acquireExecutionWork(ctx context.Context) (func(), error) {
	return s.acquireLedgerWorkAdmission(ctx, s.config.Execution == nil)
}

func (s *Service) acquireLedgerWorkAdmission(ctx context.Context, pinGeneration bool) (func(), error) {
	s.lifecycleMu.Lock()
	if s.lifecycleState == serviceStopping || s.lifecycleState == serviceStopped {
		s.lifecycleMu.Unlock()
		return nil, errServiceNotRunning
	}
	s.ledgerWorkWG.Add(1)
	s.lifecycleMu.Unlock()
	releaseMemory, err := s.acquirePersistenceAdmission(ctx)
	if err != nil {
		s.ledgerWorkWG.Done()
		return nil, err
	}
	releaseState := func() {}
	if pinGeneration {
		releaseState, err = s.AcquireStateAdmission(ctx)
		if err != nil {
			releaseMemory()
			s.ledgerWorkWG.Done()
			return nil, err
		}
	}
	return sync.OnceFunc(func() {
		releaseState()
		releaseMemory()
		s.ledgerWorkWG.Done()
	}), nil
}

func (s *Service) acquirePersistenceAdmission(ctx context.Context) (func(), error) {
	p := &s.persistenceWorker
	if s.config.PersistenceMemory == nil {
		return func() {}, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.persistMu.Lock()
		if p.persistAdmissionStop {
			p.persistMu.Unlock()
			return nil, errServiceNotRunning
		}
		pending := len(p.persistQueue) + p.persistAdmissions
		if p.persistActive != nil {
			pending++
		}
		if !p.persistStarted || pending < persistenceAdmissionLimit {
			p.persistAdmissions++
			p.persistMu.Unlock()
			return sync.OnceFunc(func() {
				p.persistMu.Lock()
				p.persistAdmissions--
				p.signalCapacityLocked()
				p.persistMu.Unlock()
			}), nil
		}
		if p.persistCapacityWake == nil {
			p.persistCapacityWake = make(chan struct{})
		}
		wake := p.persistCapacityWake
		p.persistMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (p *persistenceWorker) signalCapacityLocked() {
	if p.persistCapacityWake != nil {
		close(p.persistCapacityWake)
		p.persistCapacityWake = nil
	}
}

func (p *persistenceWorker) stopAdmissions() {
	if p == nil {
		return
	}
	p.persistMu.Lock()
	p.persistAdmissionStop = true
	p.signalCapacityLocked()
	p.persistMu.Unlock()
}
