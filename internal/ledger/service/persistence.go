package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync/atomic"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

var validatedTipKey = nodestore.Hash256(sha512half.Sum([]byte("go-xrpl validated ledger tip")))

func (s *Service) persistValidatedLedger(ctx context.Context, l *ledger.Ledger, updateTip bool) error {
	return s.persistLedger(ctx, l, updateTip, false)
}

func (s *Service) persistLedger(ctx context.Context, l *ledger.Ledger, updateTip, allowTipReplacement bool) error {
	seq := l.Sequence()
	var token uint64
	if l.IsValidated() {
		token = s.beginValidatedPersistence(seq, l.Hash())
	}
	err := s.persistValidatedLedgerAtToken(ctx, l, updateTip, token, allowTipReplacement, nil, s.replayPersistenceProof(l.Header()))
	if token != 0 {
		s.recordPersistOutcome(seq, err == nil)
		if err == nil {
			s.retryFailedValidatedLedgersAfterPersist(seq)
		}
	}
	return err
}

func (s *Service) persistValidatedLedgerAtToken(
	ctx context.Context,
	l *ledger.Ledger,
	updateTip bool,
	token uint64,
	allowTipReplacement bool,
	canceled func() bool,
	proof *replayCompletenessRecord,
) error {
	seq := l.Sequence()
	var persistErr error
	var recertificationNeeded bool
	s.retentionWriteMu.RLock()
	defer s.retentionWriteMu.RUnlock()
	if (!updateTip || canceled != nil) && s.belowRetentionFloor(seq) {
		if token != 0 {
			s.recordValidatedPersistence(seq, token, false)
		}
		return nil
	}

	if s.nodeStore != nil {
		if err := s.persistToNodeStore(ctx, l, seq); err != nil {
			persistErr = err
		}
	}
	if canceled != nil && canceled() {
		return nil
	}

	if s.relationalDB != nil || (updateTip && s.nodeStore != nil) {
		s.canonicalPersistMu.Lock()
		defer s.canonicalPersistMu.Unlock()
		if canceled != nil && canceled() {
			return nil
		}
	}
	if persistErr == nil && s.relationalDB != nil {
		persistErr = s.persistToRelationalDB(ctx, l)
	}
	if canceled != nil && canceled() {
		return nil
	}
	dataSaved := persistErr == nil
	if dataSaved && updateTip && s.nodeStore != nil {
		persistErr = s.persistValidatedTipLocked(ctx, l, allowTipReplacement)
		if persistErr == nil {
			recertificationNeeded = s.tryAdvanceValidatedStateBaseProof(ctx, l, proof)
		}
	}

	if l.IsValidated() {
		s.recordValidatedPersistenceForLedger(seq, token, dataSaved, l)
		if persistErr == nil && s.nodeStore != nil && s.shamapFamily != nil {
			s.markFastLoadCheckpointEligible()
			if recertificationNeeded {
				s.requestStateBaseRecertificationIfNeeded(l)
			}
		}
	}
	return persistErr
}

// persistJob is a ledger write, an eviction cleanup, or a FIFO barrier.
type persistJob struct {
	l               *ledger.Ledger
	evictedTip      *evictedLedgerTip
	done            chan struct{}
	validated       bool
	tipOnly         bool
	canceled        atomic.Bool
	updatesTip      atomic.Bool
	completionToken uint64
	proof           *replayCompletenessRecord
}

type evictedLedgerTip struct {
	sequence uint32
	hash     [32]byte
}

type ledgerPersistKind uint8

const (
	persistNodeData ledgerPersistKind = iota
	persistValidatedTip
	persistValidatedHistory
	persistAcquiredHistory
	retryValidatedHistory
)

func (p *persistenceWorker) enqueuePersist(l *ledger.Ledger) {
	p.enqueueLedgerPersist(l, persistValidatedTip)
}

func (p *persistenceWorker) enqueueValidatedHistoryPersist(l *ledger.Ledger) {
	p.enqueueLedgerPersist(l, persistValidatedHistory)
}

// residentRepairLimit bounds the holes one validated ledger re-queues, so
// recovery cannot crowd consensus out of the persistence FIFO.
const residentRepairLimit = 4

func (s *Service) retryFailedValidatedLedgersAfterPersist(seq uint32) {
	window := s.ledgerCacheSize()
	var failed []*ledger.Ledger
	s.completeMu.RLock()
	for candidate, l := range s.failedValidatedLedgers {
		if candidate < seq && seq-candidate <= window && candidate >= s.completeLedgerFloor {
			failed = append(failed, l)
		}
	}
	s.completeMu.RUnlock()
	sort.Slice(failed, func(i, j int) bool { return failed[i].Sequence() > failed[j].Sequence() })
	for _, l := range failed[:min(len(failed), residentRepairLimit)] {
		s.enqueueLedgerPersist(l, retryValidatedHistory)
	}
}

// RequeueResidentLedger queues persistence of the resident validated ledger
// with the given hash. It reports false when that ledger is not held.
func (s *Service) RequeueResidentLedger(seq uint32, hash [32]byte) bool {
	s.historyComponent.mu.RLock()
	defer s.historyComponent.mu.RUnlock()
	l := s.ledgerHistory[seq]
	if l == nil || l.Hash() != hash || !l.IsValidated() {
		return false
	}
	s.enqueueValidatedHistoryPersist(l)
	return true
}

func (p *persistenceWorker) enqueueNodePersist(l *ledger.Ledger) {
	p.enqueueLedgerPersist(l, persistNodeData)
}

func (p *persistenceWorker) enqueueLedgerPersist(l *ledger.Ledger, kind ledgerPersistKind) {
	if l == nil {
		return
	}
	s := p.service
	seq := l.Sequence()
	validated := kind != persistNodeData
	updatesTip := kind == persistValidatedTip
	p.persistMu.Lock()
	if kind == retryValidatedHistory {
		s.completeMu.RLock()
		stillFailed := s.failedValidatedLedgers[seq] == l
		s.completeMu.RUnlock()
		if !stillFailed {
			p.persistMu.Unlock()
			return
		}
	}
	if validated {
		if s.belowRetentionFloor(seq) {
			p.persistMu.Unlock()
			return
		}
		if existing := p.validatedPersistJobs[seq]; existing != nil {
			if existing.l != nil && existing.l.Hash() == l.Hash() {
				if updatesTip {
					existing.updatesTip.Store(true)
				}
				p.persistMu.Unlock()
				return
			}
			if !updatesTip && existing.updatesTip.Load() {
				p.persistMu.Unlock()
				return
			}
			existing.canceled.Store(true)
			delete(p.validatedPersistJobs, seq)
		}
		if kind != persistAcquiredHistory && s.hasCompleteLedger(l) && !updatesTip {
			p.persistMu.Unlock()
			return
		}
	}
	job := &persistJob{
		l:         l,
		validated: validated,
		proof:     s.replayPersistenceProof(l.Header()),
	}
	job.updatesTip.Store(updatesTip)
	if validated {
		if kind != persistAcquiredHistory && s.hasCompleteLedger(l) {
			job.tipOnly = true
		} else if l.IsValidated() {
			job.completionToken = s.beginValidatedPersistence(seq, l.Hash())
		}
		p.validatedPersistJobs[seq] = job
	}
	if !p.persistStarted {
		p.persistMu.Unlock()
		s.runPersistJob(job)
		return
	}
	if p.persistStopping && kind != retryValidatedHistory {
		if validated {
			job.canceled.Store(true)
			delete(p.validatedPersistJobs, seq)
		}
		p.persistMu.Unlock()
		if validated && !job.tipOnly {
			s.invalidateCompleteLedger(seq)
		}
		s.logger.Warn("persist skipped: service stopping", "seq", seq)
		return
	}
	// A retry caused by a job ahead of a flush barrier belongs to that flush.
	index := len(p.persistQueue)
	if kind == retryValidatedHistory {
		for i, queued := range p.persistQueue {
			if queued.done != nil {
				index = i
				break
			}
		}
	}
	p.persistQueue = slices.Insert(p.persistQueue, index, job)
	p.signalPersistLocked()
	p.persistMu.Unlock()
}

func (p *persistenceWorker) signalPersistLocked() {
	select {
	case p.persistWake <- struct{}{}:
	default:
	}
}

func (p *persistenceWorker) start() {
	p.persistMu.Lock()
	if p.persistStarted || p.persistStopping {
		p.persistMu.Unlock()
		return
	}
	p.persistStarted = true
	p.persistWG.Add(1)
	p.persistMu.Unlock()
	go p.runPersistWorker()
}

func (p *persistenceWorker) stop() {
	p.persistMu.Lock()
	startPending := !p.persistStarted && len(p.persistQueue) != 0
	if startPending {
		p.persistStarted = true
		p.persistWG.Add(1)
	}
	started := p.persistStarted
	if !p.persistStopping {
		p.persistStopping = true
		if started {
			p.signalPersistLocked()
		}
	}
	p.persistMu.Unlock()
	if startPending {
		go p.runPersistWorker()
	}
	if started {
		p.persistWG.Wait()
	}
}

func (s *Service) FlushPersists() {
	_ = s.flushPersists(context.Background())
}

func (s *Service) flushPersists(ctx context.Context) error {
	return s.persistenceWorker.flushPersists(ctx)
}

func (p *persistenceWorker) flushPersists(ctx context.Context) error {
	done := make(chan struct{})
	p.persistMu.Lock()
	startPending := !p.persistStarted && !p.persistStopping && len(p.persistQueue) != 0
	if startPending {
		p.persistStarted = true
		p.persistWG.Add(1)
	}
	if !p.persistStarted || p.persistStopping {
		p.persistMu.Unlock()
		return nil
	}
	p.persistQueue = append(p.persistQueue, &persistJob{done: done})
	p.signalPersistLocked()
	p.persistMu.Unlock()
	if startPending {
		go p.runPersistWorker()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop drains the persistence queue and joins the worker, guaranteeing every
// ledger persist enqueued before Stop is durable before the caller
// closes the underlying node/relational stores. Idempotent and safe on a
// never-started Service. Must be called before those stores are closed.
func (s *Service) Stop() {
	s.lifecycleMu.Lock()
	switch s.lifecycleState {
	case serviceStopping:
		done := s.stopDone
		s.lifecycleMu.Unlock()
		<-done
		return
	case serviceStopped:
		s.lifecycleMu.Unlock()
		return
	}
	s.lifecycleState = serviceStopping
	if s.memoryAdmissionStop != nil {
		close(s.memoryAdmissionStop)
	}
	s.stopDone = make(chan struct{})
	done := s.stopDone
	replayRecoveryCancel := s.replayRecoveryCancel
	if s.lifecycleCtx == nil {
		s.lifecycleCtx, s.lifecycleCancel = context.WithCancel(context.Background())
	}
	lifecycleCancel := s.lifecycleCancel
	s.lifecycleMu.Unlock()
	if lifecycleCancel != nil {
		lifecycleCancel()
	}
	detachedIngressDone := s.detachedIngress.stop()
	// Detached preferred-ledger preparation owns a lifecycle admission until
	// publication or discard. Cancel it before waiting for ledgerWorkWG so
	// shutdown cannot strand parked ingress behind a successor that will never
	// publish.
	s.cancelPreferredTransition()
	s.stopSpeculativeSuccessors()
	s.signalReplayPreparedSlots()
	s.persistenceWorker.stopAdmissions()
	if replayRecoveryCancel != nil {
		replayRecoveryCancel()
	}
	s.StopStateBaseRecertification()

	// Drain validation lookups before the underlying stores can be closed, then
	// wait for an in-flight submission or ledger transition. Queued open-ledger
	// callers recheck the stopping state after acquiring openLedgerMu and cannot
	// mutate state, regardless of which side of this barrier they acquire it on.
	s.validationWG.Wait()
	s.consensusWG.Wait()
	s.ledgerWorkWG.Wait()
	<-detachedIngressDone
	s.openLedgerMu.LockRole(openLedgerShutdown)
	s.openLedgerMu.Unlock()
	s.stopNodeStoreSweeper()
	s.persistenceWorker.stop()
	s.retryTipOnShutdown()
	s.eventPublisher.stop()
	s.clearRelayCache()
	s.mu.Lock()
	s.clearFastLoadBaseLocked()
	s.mu.Unlock()
	// Retain the final proof for stopped-service checkpoint preparation.
	s.validatedStateBaseMu.Lock()
	s.validatedStateBaseCandidate = nil
	s.validatedStateBaseMu.Unlock()

	s.lifecycleMu.Lock()
	s.lifecycleState = serviceStopped
	close(done)
	s.lifecycleMu.Unlock()
}

func (p *persistenceWorker) runPersistWorker() {
	defer p.persistWG.Done()
	for {
		p.persistMu.Lock()
		if len(p.persistQueue) > 0 {
			job := p.persistQueue[0]
			p.persistQueue[0] = nil
			p.persistQueue = p.persistQueue[1:]
			p.persistActive = job
			p.persistMu.Unlock()
			p.service.runPersistJob(job)
			p.persistMu.Lock()
			p.persistActive = nil
			p.signalCapacityLocked()
			p.persistMu.Unlock()
			continue
		}
		stopping := p.persistStopping
		p.persistMu.Unlock()
		if stopping {
			return
		}
		<-p.persistWake
	}
}

func (s *Service) runPersistJob(job *persistJob) {
	if job == nil {
		return
	}
	if job.evictedTip != nil {
		s.invalidateEvictedValidatedTip(*job.evictedTip)
	}
	if job.l != nil {
		updateTip := job.updatesTip.Load()
		var err error
		if !job.canceled.Load() && job.tipOnly {
			if s.nodeStore != nil {
				err = s.persistValidatedTipJob(
					context.Background(),
					job.l,
					true,
					job.canceled.Load,
					job.proof,
				)
			}
		} else if !job.canceled.Load() {
			err = s.persistLedgerJob(
				context.Background(),
				job.l,
				job.validated,
				updateTip,
				job.completionToken,
				true,
				job.canceled.Load,
				job.proof,
			)
		}
		s.persistMu.Lock()
		lateTip := job.validated && !job.tipOnly && !job.canceled.Load() &&
			!updateTip && job.updatesTip.Load()
		if err == nil && lateTip && s.nodeStore != nil {
			s.persistMu.Unlock()
			err = s.persistValidatedTipJob(
				context.Background(),
				job.l,
				true,
				job.canceled.Load,
				job.proof,
			)
			s.persistMu.Lock()
		}
		shouldRetry := false
		if s.validatedPersistJobs[job.l.Sequence()] == job {
			delete(s.validatedPersistJobs, job.l.Sequence())
		}
		if job.validated && !job.canceled.Load() {
			// Keep invalidation's persistMu → completeMu order while recording
			// the final outcome, so a stale job cannot update repair state after
			// its registration has been removed.
			s.recordPersistOutcome(job.l.Sequence(), err == nil)
			shouldRetry = err == nil && s.persistStarted
		}
		s.persistMu.Unlock()
		if shouldRetry {
			s.retryFailedValidatedLedgersAfterPersist(job.l.Sequence())
		}
		if err != nil {
			s.logger.Error("failed to persist ledger; chain advance continues",
				"seq", job.l.Sequence(), "err", err)
		}
	}
	if job.done != nil {
		close(job.done)
	}
}

func (s *Service) persistLedgerJob(
	ctx context.Context,
	l *ledger.Ledger,
	validated, updatesTip bool,
	completionToken uint64,
	allowTipReplacement bool,
	canceled func() bool,
	proof *replayCompletenessRecord,
) error {
	if validated {
		return s.persistValidatedLedgerAtToken(
			ctx,
			l,
			updatesTip,
			completionToken,
			allowTipReplacement,
			canceled,
			proof,
		)
	}
	if s.nodeStore == nil {
		return nil
	}
	return s.persistToNodeStore(ctx, l, l.Sequence())
}

func (p *persistenceWorker) beginNodePersist() {
	p.promotionMu.Lock()
	if p.nodePersists == 0 {
		p.nodePersistIdle = make(chan struct{})
	}
	p.nodePersists++
	p.promotionMu.Unlock()
}

func (p *persistenceWorker) endNodePersist() {
	p.promotionMu.Lock()
	p.nodePersists--
	if p.nodePersists == 0 {
		close(p.nodePersistIdle)
	}
	p.promotionMu.Unlock()
}

// Admission waits are cancelable and never hold a NodeStore lock. Already
// admitted batches can finish; active ledger persists prevent further batches.
func (p *persistenceWorker) waitForNodePersists(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.promotionMu.Lock()
		active, idle := p.nodePersists, p.nodePersistIdle
		p.promotionMu.Unlock()
		if active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle:
		}
	}
}

// persistToNodeStore writes state and transaction deltas before the header.
func (s *Service) persistToNodeStore(ctx context.Context, l *ledger.Ledger, seq uint32) error {
	s.beginNodePersist()
	defer s.endNodePersist()
	family, ok := s.shamapFamily.(nodeStoreFamily)
	useFamily := ok && family.UsesDatabase(s.nodeStore.database())
	var releaseClean func()
	if useFamily {
		releaseClean = l.PrepareCleanResidencyRelease(family)
	}
	store := func(nodeType nodestore.NodeType) func([]shamap.FlushEntry) error {
		return func(entries []shamap.FlushEntry) error {
			if useFamily {
				return family.StoreBatch(ctx, entries)
			}
			const batchSize = 4096
			for start := 0; start < len(entries); start += batchSize {
				end := min(start+batchSize, len(entries))
				nodes := make([]*nodestore.Node, end-start)
				for i, entry := range entries[start:end] {
					nodes[i] = &nodestore.Node{
						Type:      nodeType,
						Hash:      nodestore.Hash256(entry.Hash),
						Data:      entry.Data,
						LedgerSeq: entry.LedgerSeq,
					}
				}
				if err := s.nodeStore.database().StoreBatch(ctx, nodes); err != nil {
					return err
				}
			}
			return nil
		}
	}

	if s.config.PersistenceMemory != nil {
		if err := l.StoreDirtyBatched(ctx, s.config.PersistenceMemory, store(nodestore.NodeAccount), store(nodestore.NodeTransaction)); err != nil {
			return fmt.Errorf("store ledger %d delta: %w", seq, err)
		}
	} else {
		if err := l.StoreStateDirty(store(nodestore.NodeAccount)); err != nil {
			return fmt.Errorf("store state delta for ledger %d: %w", seq, err)
		}
		if err := l.StoreTransactionDirty(store(nodestore.NodeTransaction)); err != nil {
			return fmt.Errorf("store transaction delta for ledger %d: %w", seq, err)
		}
	}

	headerData := l.SerializeHeader()
	headerNode := &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      nodestore.Hash256(l.Hash()),
		Data:      headerData,
		LedgerSeq: seq,
	}
	if err := s.nodeStore.database().Store(ctx, headerNode); err != nil {
		return fmt.Errorf("store ledger %d header: %w", seq, err)
	}

	// Single fsync once both state nodes and header are durable.
	// Sync is uninterruptible at the backend; ctx cancellation only
	// unblocks the caller (see KVDatabaseImpl.Sync).
	if err := s.nodeStore.database().Sync(ctx); err != nil {
		return fmt.Errorf("sync ledger %d: %w", seq, err)
	}
	if releaseClean != nil {
		releaseClean()
	}
	return nil
}

func (s *Service) persistValidatedTipJob(
	ctx context.Context,
	l *ledger.Ledger,
	allowSameSequenceReplacement bool,
	canceled func() bool,
	proof *replayCompletenessRecord,
) error {
	s.retentionWriteMu.RLock()
	defer s.retentionWriteMu.RUnlock()
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()
	if canceled != nil && canceled() {
		return nil
	}
	if err := s.persistValidatedTipLocked(ctx, l, allowSameSequenceReplacement); err != nil {
		return err
	}
	if s.tryAdvanceValidatedStateBaseProof(ctx, l, proof) {
		s.requestStateBaseRecertificationIfNeeded(l)
	}
	return nil
}

func (s *Service) retryTipOnShutdown() {
	tip := s.GetValidatedLedger()
	if tip == nil || s.nodeStore == nil {
		return
	}
	s.retentionWriteMu.RLock()
	defer s.retentionWriteMu.RUnlock()
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()
	if s.failedTipHash != tip.Hash() || !s.hasDurableCompleteLedger(tip) {
		return
	}
	err := s.persistValidatedTipLocked(context.Background(), tip, true)
	s.recordPersistOutcome(tip.Sequence(), err == nil)
	if err != nil {
		s.logger.Error("failed to persist validated tip before shutdown", "seq", tip.Sequence(), "err", err)
	}
}

func (s *Service) persistValidatedTipLocked(
	ctx context.Context,
	l *ledger.Ledger,
	allowSameSequenceReplacement bool,
) (err error) {
	hash := l.Hash()
	defer func() {
		if err != nil {
			s.failedTipHash = hash
		} else if s.failedTipHash == hash {
			s.failedTipHash = [32]byte{}
		}
	}()
	current, err := s.nodeStore.database().Fetch(ctx, validatedTipKey)
	if err != nil {
		return fmt.Errorf("fetch validated ledger tip: %w", err)
	}
	if current != nil && current.Type == nodestore.NodeLedger && len(current.Data) == 32 {
		switch {
		case current.LedgerSeq > l.Sequence():
			return nil
		case current.LedgerSeq == l.Sequence():
			if bytes.Equal(current.Data, hash[:]) {
				// A failed sync leaves the previous tip write readable.
				return s.nodeStore.database().Sync(ctx)
			}
			if !allowSameSequenceReplacement {
				return fmt.Errorf("validated ledger tip %d conflicts with persisted hash", l.Sequence())
			}
		}
	}
	if err := s.nodeStore.database().Store(ctx, &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      validatedTipKey,
		Data:      append([]byte(nil), hash[:]...),
		LedgerSeq: l.Sequence(),
	}); err != nil {
		return fmt.Errorf("store validated ledger tip %d: %w", l.Sequence(), err)
	}
	if err := s.nodeStore.database().Sync(ctx); err != nil {
		return fmt.Errorf("sync validated ledger tip %d: %w", l.Sequence(), err)
	}
	return nil
}

func (s *Service) invalidatePersistedValidatedTip(start, end uint32) {
	s.invalidatePersistedValidatedTipMatching(start, end, nil)
}

func (s *Service) invalidatePersistedValidatedTipHash(seq uint32, hash [32]byte) {
	s.invalidatePersistedValidatedTipMatching(seq, seq, &hash)
}

func (s *Service) invalidatePersistedValidatedTipMatching(start, end uint32, expectedHash *[32]byte) {
	if s.nodeStore == nil || start > end {
		return
	}
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()
	s.invalidatePersistedValidatedTipMatchingLocked(start, end, expectedHash)
}

func (s *Service) invalidateEvictedValidatedTip(tip evictedLedgerTip) {
	if s.nodeStore == nil {
		return
	}
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()
	// A direct persist can overtake the FIFO. Its completion is published
	// under canonicalPersistMu, so cleanup cannot erase a newly durable tip.
	s.completeMu.RLock()
	_, pending := s.completeLedgerTokens[tip.sequence]
	durable := s.shamapFamily != nil && !pending &&
		s.completedLedgers != nil && s.completedLedgers.contains(tip.sequence) &&
		s.completeLedgerHashes[tip.sequence] == tip.hash
	s.completeMu.RUnlock()
	if durable {
		return
	}
	s.invalidatePersistedValidatedTipMatchingLocked(tip.sequence, tip.sequence, &tip.hash)
}

func (s *Service) invalidatePersistedValidatedTipMatchingLocked(start, end uint32, expectedHash *[32]byte) {
	ctx := context.Background()
	current, err := s.nodeStore.database().Fetch(ctx, validatedTipKey)
	if err != nil {
		s.logger.Error("failed to inspect validated ledger tip during invalidation", "err", err)
		return
	}
	if current == nil || current.Type != nodestore.NodeLedger || len(current.Data) != 32 ||
		current.LedgerSeq < start || current.LedgerSeq > end {
		return
	}
	if expectedHash != nil && !bytes.Equal(current.Data, expectedHash[:]) {
		return
	}
	s.invalidateFastLoadCheckpointEligibility("durable validated tip invalidated")
	if err := s.nodeStore.database().Store(ctx, &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      validatedTipKey,
		Data:      make([]byte, 32),
		LedgerSeq: 0,
	}); err != nil {
		s.logger.Error("failed to invalidate validated ledger tip", "seq", current.LedgerSeq, "err", err)
		return
	}
	if err := s.nodeStore.database().Sync(ctx); err != nil {
		s.logger.Error("failed to sync invalidated validated ledger tip", "seq", current.LedgerSeq, "err", err)
	}
}

const (
	refreshHealthCheckInterval = 1000
	refreshHealthCheckPeriod   = 10 * time.Millisecond
)

func (s *Service) refreshGenerationState(
	ctx context.Context,
	root [32]byte,
	sequence uint32,
	generations nodestore.GenerationDatabase,
	checkpoint func(context.Context, time.Duration) error,
) error {
	return s.refreshGenerationStateWithBatch(
		ctx,
		root,
		sequence,
		generations,
		checkpoint,
		resolveOnlineDeleteRefreshWorkers(),
		storedSHAMapPromotionBatchNodes,
		storedSHAMapPromotionBatchBytes,
	)
}

func (s *Service) refreshGenerationStateWithBatch(
	ctx context.Context,
	root [32]byte,
	sequence uint32,
	generations nodestore.GenerationDatabase,
	checkpoint func(context.Context, time.Duration) error,
	workers int,
	batchNodes int,
	batchBytes int,
) (err error) {
	startedAt := time.Now()
	progress := newOnlineDeleteRefreshProgress(
		s.logger,
		s.nodeStore.database(),
		root,
		shamap.TypeState,
		sequence,
		startedAt,
	)
	defer func() { progress.finish(time.Now(), err) }()

	progressTicker := time.NewTicker(storedSHAMapVerificationLogInterval)
	defer progressTicker.Stop()
	var checkpointTicker *time.Ticker
	var checkpointTicks <-chan time.Time
	if checkpoint != nil {
		checkpointTicker = time.NewTicker(refreshHealthCheckPeriod)
		checkpointTicks = checkpointTicker.C
		defer checkpointTicker.Stop()
	}

	control := storedSHAMapWalkControl{
		progress:        progress,
		progressTicks:   progressTicker.C,
		checkpoint:      checkpoint,
		checkpointTicks: checkpointTicks,
		now:             time.Now,
	}
	if batchNodes > 0 {
		if batches, ok := generations.(nodestore.BatchGenerationDatabase); ok {
			promotionMetrics := &onlineDeleteRefreshPromotionMetrics{}
			progress.promotionMetrics = promotionMetrics
			control.batchFetch = func(ctx context.Context, hashes []nodestore.Hash256, maxBytes int) ([]*nodestore.Node, kvstore.PromotionStats, error) {
				waitStartedAt := time.Now()
				waitErr := s.waitForNodePersists(ctx)
				waitElapsed := time.Since(waitStartedAt)
				if waitErr != nil {
					promotionMetrics.recordWait(waitElapsed)
					return nil, kvstore.PromotionStats{}, waitErr
				}
				fetchStartedAt := time.Now()
				nodes, stats, fetchErr := batches.FetchBatchForPromotion(ctx, hashes, maxBytes)
				fetchElapsed := time.Since(fetchStartedAt)
				// Record the backend's counters before propagating its error. The
				// backend may return useful partial-prefix diagnostics on failure.
				promotionMetrics.record(waitElapsed, fetchElapsed, stats, len(nodes), fetchErr)
				return nodes, stats, fetchErr
			}
			control.batchNodes = batchNodes
			control.batchBytes = batchBytes
		}
	}
	err = s.walkStoredSHAMapConcurrentWithFetch(
		ctx,
		root,
		shamap.TypeState,
		generations.FetchForPromotion,
		workers,
		control,
		nil,
	)
	if err != nil {
		return err
	}
	return s.nodeStore.database().Sync(ctx)
}

// RefreshValidatedState preserves the complete live state tree before online
// deletion retires older node-store records. Rotating stores promote archive
// reads into their writable generation; legacy stores re-stamp records in place.
// It returns the validated sequence whose state was preserved.
func (s *Service) RefreshValidatedState(
	ctx context.Context,
	minimumSeq uint32,
	checkpoint func(context.Context, time.Duration) error,
) (uint32, error) {
	s.mu.RLock()
	validated := s.validatedLedger
	s.mu.RUnlock()
	if validated == nil || validated.Sequence() < minimumSeq {
		return 0, fmt.Errorf("validated ledger is behind rotation target %d", minimumSeq)
	}
	if err := s.flushPersists(ctx); err != nil {
		return 0, err
	}
	seq := validated.Sequence()

	if generations := s.nodeStore.capabilities().generations; generations != nil {
		skipRefresh, err := generations.CanRotateWithoutRefresh(ctx)
		if err != nil {
			return 0, err
		}
		if skipRefresh {
			if err := s.nodeStore.database().Sync(ctx); err != nil {
				return 0, err
			}
			return seq, nil
		}
		root, err := validated.StateMapHash()
		if err != nil {
			return 0, err
		}
		if err := s.refreshGenerationState(ctx, root, seq, generations, checkpoint); err != nil {
			return 0, err
		}
		return seq, nil
	}

	root, err := validated.StateMapHash()
	if err != nil {
		return 0, err
	}
	const batchSize = 4096
	batch := make([]*nodestore.Node, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.nodeStore.database().StoreBatch(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	visited := 0
	checkpointStarted := time.Now()
	err = s.walkStoredSHAMap(ctx, root, shamap.TypeState, func(hash [32]byte, node *nodestore.Node) error {
		batch = append(batch, &nodestore.Node{
			Type:      nodestore.NodeAccount,
			Hash:      nodestore.Hash256(hash),
			Data:      node.Data,
			LedgerSeq: seq,
		})
		if len(batch) == batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		visited++
		work := time.Since(checkpointStarted)
		if checkpoint != nil &&
			(visited%refreshHealthCheckInterval == 0 || work >= refreshHealthCheckPeriod) {
			if err := checkpoint(ctx, work); err != nil {
				return err
			}
			checkpointStarted = time.Now()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := flush(); err != nil {
		return 0, err
	}
	if err := s.nodeStore.database().Sync(ctx); err != nil {
		return 0, err
	}
	return seq, nil
}

// RepairLedgerTransactions rebuilds the relational ledger and transaction
// indexes from the canonical ledger. Nodes without relational persistence have
// nothing to repair.
func (s *Service) RepairLedgerTransactions(ctx context.Context, seq uint32) error {
	if s == nil || s.relationalDB == nil {
		return nil
	}
	l, err := s.getLedgerBySequence(ctx, seq)
	if err != nil {
		return err
	}
	s.retentionWriteMu.RLock()
	defer s.retentionWriteMu.RUnlock()
	if s.belowRetentionFloor(seq) {
		return svcerr.ErrLedgerNotFound
	}
	if s.nodeStore != nil {
		if err := s.persistToNodeStore(ctx, l, seq); err != nil {
			return err
		}
	}
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()
	return s.persistToRelationalDB(ctx, l)
}

// persistToRelationalDB materializes a validated ledger and all of its
// transaction indexes before handing the immutable unit to the relational
// backend. The backend owns commit ordering and retry safety.
func (s *Service) persistToRelationalDB(ctx context.Context, l *ledger.Ledger) error {
	h := l.Header()

	stateHash, err := l.StateMapHash()
	if err != nil {
		return fmt.Errorf("compute state map hash: %w", err)
	}
	if stateHash == ([32]byte{}) {
		return errors.New("compute state map hash: empty state root")
	}
	if stateHash != h.AccountHash {
		return fmt.Errorf("state map hash %x does not match ledger header %x", stateHash, h.AccountHash)
	}
	txHash, err := l.TxMapHash()
	if err != nil {
		return fmt.Errorf("compute transaction map hash: %w", err)
	}
	if txHash != h.TxHash {
		return fmt.Errorf("transaction map hash %x does not match ledger header %x", txHash, h.TxHash)
	}

	ledgerInfo := relationaldb.LedgerInfo{
		Hash:            relationaldb.Hash(l.Hash()),
		Sequence:        relationaldb.LedgerIndex(h.LedgerIndex),
		ParentHash:      relationaldb.Hash(h.ParentHash),
		AccountHash:     relationaldb.Hash(stateHash),
		TransactionHash: relationaldb.Hash(txHash),
		TotalCoins:      relationaldb.Amount(h.Drops),
		CloseTime:       h.CloseTime,
		ParentCloseTime: h.ParentCloseTime,
		CloseTimeRes:    int32(h.CloseTimeResolution),
		CloseFlags:      uint32(h.CloseFlags),
	}

	seq := relationaldb.LedgerIndex(l.Sequence())

	indexed := make([]relationaldb.IndexedTransaction, 0)
	var loopErr error
	err = l.ForEachTransactionContext(ctx, func(txHashBytes [32]byte, txData []byte) bool {
		txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(txData)
		if err != nil {
			loopErr = fmt.Errorf("split transaction %x: %w", txHashBytes[:8], err)
			return false
		}

		var accountID relationaldb.AccountID
		parsedTransaction, err := tx.ParseFromBinary(txBlob)
		if err != nil {
			loopErr = fmt.Errorf("decode transaction %x: %w", txHashBytes[:8], err)
			return false
		}
		computedHash, err := tx.ComputeTransactionHash(parsedTransaction)
		if err != nil {
			loopErr = fmt.Errorf("hash transaction %x: %w", txHashBytes[:8], err)
			return false
		}
		if computedHash != txHashBytes {
			loopErr = fmt.Errorf("transaction hash %x does not match map key %x", computedHash, txHashBytes)
			return false
		}
		account := parsedTransaction.GetCommon().Account
		if account != "" {
			_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
			if err != nil || len(accountBytes) != len(accountID) {
				loopErr = fmt.Errorf("decode transaction %x Account: %w", txHashBytes[:8], errors.Join(err, relationaldb.ErrInvalidData))
				return false
			}
			copy(accountID[:], accountBytes)
		}

		affected := map[relationaldb.AccountID]struct{}{}

		txnSeq, ok := tx.TransactionIndexFromMetadata(metaBlob)
		if !ok {
			loopErr = fmt.Errorf("decode transaction metadata %x: missing or invalid TransactionIndex", txHashBytes[:8])
			return false
		}
		metaJSON, err := binarycodec.Decode(hex.EncodeToString(metaBlob))
		if err != nil {
			loopErr = fmt.Errorf("decode transaction metadata %x: %w", txHashBytes[:8], err)
			return false
		}
		if result, ok := metaJSON["TransactionResult"].(string); !ok || result == "" {
			loopErr = fmt.Errorf("decode transaction metadata %x: missing or invalid TransactionResult", txHashBytes[:8])
			return false
		}
		if err := addMetaAffectedAccounts(metaJSON, affected); err != nil {
			loopErr = fmt.Errorf("decode transaction metadata %x: %w", txHashBytes[:8], err)
			return false
		}

		indexed = append(indexed, relationaldb.IndexedTransaction{
			Transaction: relationaldb.TransactionInfo{
				Hash:      relationaldb.Hash(txHashBytes),
				LedgerSeq: seq,
				TxnSeq:    txnSeq,
				Status:    "validated",
				RawTxn:    txBlob,
				TxnMeta:   metaBlob,
				Account:   accountID,
			},
			Accounts: sortedAccountIDs(affected),
		})
		return true
	})
	if err != nil {
		return fmt.Errorf("iterate transaction map: %w", err)
	}
	if loopErr != nil {
		return loopErr
	}
	return s.relationalDB.PersistValidatedLedger(ctx, relationaldb.ValidatedLedger{
		Ledger:       ledgerInfo,
		Transactions: indexed,
	})
}

// addMetaAffectedAccounts collects every account a transaction's metadata
// affected into `into`, mirroring rippled's TxMeta::getAffectedAccounts: for
// each affected node it reads NewFields (CreatedNode) or FinalFields
// (Modified/DeletedNode) and adds every account-typed field, the issuer of any
// LowLimit/HighLimit/TakerPays/TakerGets amount, and the issuer encoded in any
// MPTokenIssuanceID. In decoded metadata JSON account fields are plain
// classic-address strings and those amounts are objects, so a
// string-decodes-as-address test isolates the account fields.
func addMetaAffectedAccounts(metaJSON map[string]any, into map[relationaldb.AccountID]struct{}) error {
	rawNodes, exists := metaJSON["AffectedNodes"]
	if !exists {
		return errors.New("missing AffectedNodes")
	}
	nodes, ok := rawNodes.([]any)
	if !ok {
		return errors.New("AffectedNodes is not an array")
	}
	addAddr := func(s string) {
		if _, b, err := addresscodec.DecodeClassicAddressToAccountID(s); err == nil && len(b) == 20 {
			var id relationaldb.AccountID
			copy(id[:], b)
			if !id.IsZero() {
				into[id] = struct{}{}
			}
		}
	}
	// An MPTokenIssuanceID is the 24-byte (4-byte sequence ++ 20-byte issuer)
	// hex of an MPT issuance; index its issuer so MPToken activity is queryable
	// by the issuing account.
	addMPTIssuer := func(hexID string) error {
		mptID, err := mptutil.DecodeID(hexID)
		if err != nil {
			return err
		}
		id := relationaldb.AccountID(mptutil.Issuer(mptID))
		if !id.IsZero() {
			into[id] = struct{}{}
		}
		return nil
	}
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			return errors.New("affected node is not an object")
		}
		found := false
		for _, wrapper := range []string{"CreatedNode", "ModifiedNode", "DeletedNode"} {
			inner, exists := node[wrapper]
			if !exists {
				continue
			}
			found = true
			im, ok := inner.(map[string]any)
			if !ok {
				return fmt.Errorf("%s is not an object", wrapper)
			}
			fieldsKey := "FinalFields"
			if wrapper == "CreatedNode" {
				fieldsKey = "NewFields"
			}
			rawFields, exists := im[fieldsKey]
			if !exists {
				continue
			}
			fields, ok := rawFields.(map[string]any)
			if !ok {
				return fmt.Errorf("%s %s is not an object", wrapper, fieldsKey)
			}
			for name, val := range fields {
				switch v := val.(type) {
				case string:
					if name == "MPTokenIssuanceID" {
						if err := addMPTIssuer(v); err != nil {
							return err
						}
					} else {
						addAddr(v)
					}
				case map[string]any:
					switch name {
					case "LowLimit", "HighLimit", "TakerPays", "TakerGets":
						if mptID, ok := v["mpt_issuance_id"].(string); ok {
							if err := addMPTIssuer(mptID); err != nil {
								return err
							}
						} else if iss, ok := v["issuer"].(string); ok {
							addAddr(iss)
						}
					}
				}
			}
			break
		}
		if !found {
			return errors.New("affected node has no recognized node type")
		}
	}
	return nil
}

// sortedAccountIDs returns the set's account IDs in ascending byte order so
// account_tx rows are persisted deterministically.
func sortedAccountIDs(set map[relationaldb.AccountID]struct{}) []relationaldb.AccountID {
	out := make([]relationaldb.AccountID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}
