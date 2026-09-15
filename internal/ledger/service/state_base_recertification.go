package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// Guarded by validatedStateBaseMu; tentative successors are never public proofs.
type stateBaseRecertification struct {
	tip   header.LedgerHeader
	epoch uint64
}

// BeginStateBaseRetentionChange fences retention-source updates against
// certificate publication. The caller must release the returned guard.
func (s *Service) BeginStateBaseRetentionChange() func() {
	s.stateBaseRetentionMu.Lock()
	return s.stateBaseRetentionMu.Unlock
}

// RequestStateBaseRecertification schedules a durable walk outside consensus and
// persistence paths. Requests coalesce and failed verification is retried.
func (s *Service) RequestStateBaseRecertification() {
	s.recertificationMu.Lock()
	defer s.recertificationMu.Unlock()
	if s.recertificationStopped {
		return
	}
	if s.recertificationWake == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.recertificationCancel = cancel
		s.recertificationWake = make(chan struct{}, 1)
		s.recertificationDone = make(chan struct{})
		go s.runStateBaseRecertification(ctx, s.recertificationWake, s.recertificationDone)
	}
	select {
	case s.recertificationWake <- struct{}{}:
	default:
	}
}

// StopStateBaseRecertification rejects new requests and releases verification
// snapshots before the caller drains storage mutation producers.
func (s *Service) StopStateBaseRecertification() {
	s.recertificationMu.Lock()
	s.recertificationStopped = true
	if s.recertificationCancel != nil {
		s.recertificationCancel()
	}
	done := s.recertificationDone
	s.recertificationMu.Unlock()
	if done != nil {
		<-done
	}
}

func (s *Service) runStateBaseRecertification(ctx context.Context, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	var retry <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			if retry != nil {
				continue
			}
		case <-retry:
		}
		if timer != nil {
			timer.Stop()
		}
		retry = nil
		if err := s.recertifyValidatedStateBase(ctx); err != nil {
			s.logger.Warn("Validated state base re-certification unavailable",
				"reason", err, "required_verification", "complete uncached durable state and transaction trees")
			if ctx.Err() != nil {
				return
			}
			timer = time.NewTimer(30 * time.Second)
			retry = timer.C
		}
	}
}

func (s *Service) lockStateBasePersistence(ctx context.Context) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.canonicalPersistMu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) recertifyValidatedStateBase(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return errors.New("NodeStore cannot pin a durable generation for re-certification")
	}
	if _, ok := s.nodeStore.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	}); !ok {
		return errors.New("NodeStore has no uncached durable verification reader")
	}
	provider, ok := s.shamapFamily.(interface{ FullBelowCache() *shamap.FullBelowCache })
	if !ok || provider.FullBelowCache() == nil {
		return errors.New("SHAMap family has no durable full-below cache")
	}
	fingerprint, release, err := durable.AcquireDurableSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("pin re-certification generation: %w", err)
	}
	defer release()
	cache := provider.FullBelowCache()
	generation := cache.EnsureDurableFingerprint(fingerprint)
	if generation == 0 {
		return errors.New("re-certification generation cannot bind the full-below cache")
	}
	s.mu.RLock()
	validated, minimumOnline := s.validatedLedger, s.minimumOnlineFunc
	s.mu.RUnlock()
	if validated == nil || !validated.IsValidated() || !s.hasDurableCompleteLedger(validated) {
		return errors.New("re-certification requires a durably complete validated ledger")
	}
	h := validated.Header()
	if _, valid := newValidatedStateBaseProof(h, fingerprint); !valid {
		return errors.New("re-certification ledger identity is invalid")
	}
	if s.stateBaseBelowRetention(h.LedgerIndex, minimumOnline) {
		return errors.New("re-certification ledger is below retention")
	}
	if err := s.lockStateBasePersistence(ctx); err != nil {
		return err
	}
	err = s.durableValidatedHeader(ctx, h)
	if err == nil {
		err = s.durableValidatedTipMatches(ctx, h)
	}
	if err != nil {
		s.canonicalPersistMu.Unlock()
		return err
	}
	s.validatedStateBaseMu.Lock()
	if proof := s.validatedStateBaseProof; proof != nil &&
		s.fastLoadCheckpointState.Load() == fastLoadCheckpointEligible &&
		validatedStateBaseProofMatchesLedger(*proof, h, fingerprint) {
		s.validatedStateBaseMu.Unlock()
		s.canonicalPersistMu.Unlock()
		return nil
	}
	if s.stateBaseRecertification != nil {
		s.validatedStateBaseMu.Unlock()
		s.canonicalPersistMu.Unlock()
		return errors.New("state base re-certification is already running")
	}
	pending := &stateBaseRecertification{tip: h, epoch: s.stateBaseMutationEpoch}
	s.stateBaseRecertification = pending
	s.validatedStateBaseMu.Unlock()
	s.canonicalPersistMu.Unlock()
	defer func() {
		s.validatedStateBaseMu.Lock()
		if s.stateBaseRecertification == pending {
			s.stateBaseRecertification = nil
		}
		s.validatedStateBaseMu.Unlock()
	}()
	s.logger.Info("Validated state base re-certification started",
		"sequence", h.LedgerIndex, "state_root", fmt.Sprintf("%x", h.AccountHash),
		"fingerprint", fmt.Sprintf("%x", fingerprint), "provenance", "full durable traversal")
	metrics, err := s.verifyStoredSHAMapMeasured(ctx, h.AccountHash, shamap.TypeState)
	if err != nil {
		return fmt.Errorf("re-certify durable state tree: %w", err)
	}
	if h.TxHash != ([32]byte{}) {
		txMetrics, err := s.verifyStoredSHAMapMeasured(ctx, h.TxHash, shamap.TypeTransaction)
		if err != nil {
			return fmt.Errorf("re-certify durable transaction tree: %w", err)
		}
		metrics.nodes += txMetrics.nodes
		metrics.elapsed += txMetrics.elapsed
	}
	s.mu.RLock()
	minimumOnline = s.minimumOnlineFunc
	s.mu.RUnlock()
	if err := s.lockStateBasePersistence(ctx); err != nil {
		return err
	}
	canonicalLocked := true
	defer func() {
		if canonicalLocked {
			s.canonicalPersistMu.Unlock()
		}
	}()
	s.validatedStateBaseMu.RLock()
	tip := pending.tip
	current := s.stateBaseRecertification == pending && s.stateBaseMutationEpoch == pending.epoch
	s.validatedStateBaseMu.RUnlock()
	if !current {
		return errors.New("re-certification was invalidated or the persisted ledger chain changed")
	}
	if err := s.verifyRecertificationTip(ctx, tip); err != nil {
		return err
	}
	currentFingerprint, err := durable.DurableFingerprint(ctx)
	if err != nil {
		return err
	}
	if currentFingerprint != fingerprint {
		return errors.New("NodeStore fingerprint changed during re-certification")
	}
	if s.stateBaseBelowRetention(tip.LedgerIndex, minimumOnline) {
		return errors.New("re-certified ledger is below retention")
	}
	if !cache.BindDurableFingerprint(generation, fingerprint) {
		return errors.New("full-below cache generation changed during re-certification")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.canonicalPersistMu.Unlock()
	canonicalLocked = false
	tip, err = s.publishStateBaseRecertification(ctx, pending, fingerprint)
	if err != nil {
		return err
	}
	s.fastLoadStrictNodes.Store(metrics.nodes)
	s.fastLoadStrictElapsed.Store(uint64(metrics.elapsed))
	s.logger.Info("Fast-load checkpoint eligibility restored",
		"reason", "durable re-certification complete", "sequence", tip.LedgerIndex,
		"state_root", fmt.Sprintf("%x", tip.AccountHash), "base_sequence", h.LedgerIndex,
		"fingerprint", fmt.Sprintf("%x", fingerprint), "provenance", "full durable traversal and consecutive persisted successors")
	return nil
}

func (s *Service) publishStateBaseRecertification(
	ctx context.Context,
	pending *stateBaseRecertification,
	fingerprint [32]byte,
) (header.LedgerHeader, error) {
	s.stateBaseRetentionMu.RLock()
	defer s.stateBaseRetentionMu.RUnlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if family, ok := s.shamapFamily.(interface{ AcquireMinimumLedgerSeq() (uint32, func()) }); ok {
		_, release := family.AcquireMinimumLedgerSeq()
		defer release()
	} else if _, hasFloor := s.shamapFamily.(interface{ MinimumLedgerSeq() uint32 }); hasFloor {
		return header.LedgerHeader{}, errors.New("SHAMap family cannot fence retention during re-certification")
	}
	s.validatedStateBaseMu.Lock()
	defer s.validatedStateBaseMu.Unlock()
	if err := ctx.Err(); err != nil {
		return header.LedgerHeader{}, err
	}
	if s.stateBaseRecertification != pending || s.stateBaseMutationEpoch != pending.epoch {
		return header.LedgerHeader{}, errors.New("re-certification invalidated before publication")
	}
	tip := pending.tip
	if s.stateBaseBelowRetention(tip.LedgerIndex, s.minimumOnlineFunc) {
		return header.LedgerHeader{}, errors.New("re-certified ledger is below retention at publication")
	}
	// Persistence may lag validation, but a same-height replacement cannot
	// restore eligibility for the replaced ledger.
	if current := s.validatedLedger; current == nil || !current.IsValidated() ||
		(current.Sequence() == tip.LedgerIndex && current.Hash() != tip.Hash) {
		return header.LedgerHeader{}, errors.New("validated ledger identity changed during re-certification")
	}
	if current := s.validatedStateBaseProof; current != nil && current.sequence > tip.LedgerIndex {
		return header.LedgerHeader{}, errors.New("newer validated state base proof cannot be replaced")
	}
	proof, valid := newValidatedStateBaseProof(tip, fingerprint)
	if !valid {
		return header.LedgerHeader{}, errors.New("re-certified ledger identity is invalid")
	}
	s.validatedStateBaseProof = &proof
	s.stateBaseRecertification = nil
	s.fastLoadCheckpointState.Store(fastLoadCheckpointEligible)
	return tip, nil
}

func (s *Service) verifyRecertificationTip(ctx context.Context, h header.LedgerHeader) error {
	if err := s.durableValidatedHeader(ctx, h); err != nil {
		return err
	}
	if err := s.verifyDurableSHAMapRoot(ctx, h.AccountHash, "state"); err != nil {
		return err
	}
	if h.TxHash != ([32]byte{}) {
		if err := s.verifyDurableSHAMapRoot(ctx, h.TxHash, "transaction"); err != nil {
			return err
		}
	}
	return s.durableValidatedTipMatches(ctx, h)
}

// The caller holds canonicalPersistMu after successfully persisting the tip.
func (s *Service) advanceStateBaseRecertification(ctx context.Context, l *ledger.Ledger) {
	if l == nil || !l.IsValidated() {
		return
	}
	s.validatedStateBaseMu.RLock()
	pending := s.stateBaseRecertification
	var previous header.LedgerHeader
	if pending != nil {
		previous = pending.tip
	}
	s.validatedStateBaseMu.RUnlock()
	if pending == nil || l.Sequence() < previous.LedgerIndex {
		return
	}
	h := l.Header()
	if h.LedgerIndex == previous.LedgerIndex && h.Hash == previous.Hash {
		return
	}
	var err error
	if previous.LedgerIndex == ^uint32(0) || h.LedgerIndex != previous.LedgerIndex+1 || h.ParentHash != previous.Hash {
		err = errors.New("persisted successor does not extend the re-certification base")
	} else {
		err = s.verifyRecertificationTip(ctx, h)
	}
	s.validatedStateBaseMu.Lock()
	if s.stateBaseRecertification == pending {
		if err != nil {
			s.stateBaseRecertification = nil
		} else {
			pending.tip = h
		}
	}
	s.validatedStateBaseMu.Unlock()
	if err != nil {
		s.logger.Warn("Validated state base re-certification chain rejected", "sequence", h.LedgerIndex, "reason", err)
	}
}
