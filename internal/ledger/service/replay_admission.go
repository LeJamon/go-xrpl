package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

const replayProofCacheLimit = 4

var (
	errReplayAdmissionService = errors.New("replay admission belongs to another ledger service")
	errReplayAdmissionState   = errors.New("replay admission token is no longer usable")
	errReplayParentMismatch   = errors.New("replay admission parent does not match the derived ledger")
	errReplayPreparedInvalid  = errors.New("replay admission prepared ledger is invalid")
)

// replayCompletenessRecord is deliberately smaller than a SHAMap and contains
// no node pointers. Its fingerprint is the durable-generation identity that
// makes a record invalid after managed retention or rotation.
type replayCompletenessRecord struct {
	sequence             uint32
	ledgerHash           [32]byte
	parentHash           [32]byte
	stateRoot            [32]byte
	txRoot               [32]byte
	nodeStoreFingerprint [32]byte
	transactionComplete  bool
}

func (r replayCompletenessRecord) matchesHeader(h header.LedgerHeader, fingerprint [32]byte, transaction bool) bool {
	if r.sequence != h.LedgerIndex || r.ledgerHash != h.Hash ||
		r.parentHash != h.ParentHash || r.stateRoot != h.AccountHash ||
		r.nodeStoreFingerprint != fingerprint {
		return false
	}
	if transaction {
		return r.transactionComplete && r.txRoot == h.TxHash
	}
	return true
}

type replaySnapshotProvider interface {
	AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
}

type replayFingerprintProvider interface {
	DurableFingerprint(context.Context) ([32]byte, error)
}

type replaySnapshotPin struct {
	fingerprint [32]byte
	provider    replaySnapshotProvider
	releaseOnce sync.Once
	released    bool
	mu          sync.Mutex
	release     func()
}

func newReplaySnapshotPin(provider replaySnapshotProvider, fingerprint [32]byte, release func()) *replaySnapshotPin {
	return &replaySnapshotPin{provider: provider, fingerprint: fingerprint, release: release}
}

func (p *replaySnapshotPin) Release() {
	if p == nil {
		return
	}
	p.releaseOnce.Do(func() {
		p.mu.Lock()
		p.released = true
		release := p.release
		p.mu.Unlock()
		if release != nil {
			release()
		}
	})
}

func (p *replaySnapshotPin) active() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.released
}

// PreparedReplayParent is an opaque, generation-bound proof of a replay
// parent's state tree. It owns a durable snapshot pin until Release is called.
// The type is exported only so callers in the consensus package can retain it;
// all proof material remains private and there is no caller-supplied root API.
type PreparedReplayParent struct {
	service     *Service
	header      header.LedgerHeader
	stateRoot   [32]byte
	fingerprint [32]byte
	reusable    bool
	pin         *replaySnapshotPin
	stats       ReplayAdmissionStats
	released    bool
	releaseOnce sync.Once
	stateMu     sync.Mutex
}

// Release releases the parent's durable-generation pin. It is safe to call
// more than once and may be called after a child preparation has completed.
func (p *PreparedReplayParent) Release() {
	if p == nil {
		return
	}
	p.releaseOnce.Do(func() {
		p.stateMu.Lock()
		p.released = true
		pin := p.pin
		p.pin = nil
		p.stateMu.Unlock()
		if pin != nil {
			pin.Release()
		}
	})
}

// Stats returns the immutable work counters recorded while preparing the
// parent proof.
func (p *PreparedReplayParent) Stats() ReplayAdmissionStats {
	if p == nil {
		return ReplayAdmissionStats{}
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.stats
}

func (p *PreparedReplayParent) baseRoot(service *Service, target header.LedgerHeader) ([32]byte, [32]byte, bool) {
	if p == nil {
		return [32]byte{}, [32]byte{}, false
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.service != service || p.released || !p.reusable || p.pin == nil || !p.pin.active() {
		return [32]byte{}, [32]byte{}, false
	}
	if p.header.Hash != target.ParentHash {
		return [32]byte{}, [32]byte{}, false
	}
	return p.stateRoot, p.fingerprint, true
}

// PreparedLedger is an opaque detached ledger prepared for short publication.
// It owns immutable map snapshots, a durable pin, and the persistence-work
// admission acquired before the caller enters its commit lock.
type PreparedLedger struct {
	service      *Service
	header       header.LedgerHeader
	stateMap     *shamap.SHAMap
	txMap        *shamap.SHAMap
	fingerprint  [32]byte
	pin          *replaySnapshotPin
	releaseWork  func()
	stats        ReplayAdmissionStats
	state        preparedLedgerState
	releaseAfter bool
	mu           sync.Mutex
}

type preparedLedgerState uint8

const (
	preparedLedgerReady preparedLedgerState = iota
	preparedLedgerConsuming
	preparedLedgerPublished
	preparedLedgerReleased
)

// Release releases all resources owned by a prepared ledger. A successfully
// published token keeps only its durable snapshot pin until this method is
// called, allowing the caller to cover the subsequent activation step.
func (p *PreparedLedger) Release() {
	if p == nil {
		return
	}
	p.mu.Lock()
	switch p.state {
	case preparedLedgerReady:
		p.state = preparedLedgerReleased
		p.releaseAfter = true
		releaseWork := p.releaseWork
		pin := p.pin
		p.releaseWork = nil
		p.pin = nil
		p.stateMap = nil
		p.txMap = nil
		p.header = header.LedgerHeader{}
		p.mu.Unlock()
		if releaseWork != nil {
			releaseWork()
		}
		if pin != nil {
			pin.Release()
		}
		return
	case preparedLedgerConsuming:
		p.releaseAfter = true
		p.mu.Unlock()
		return
	case preparedLedgerPublished:
		p.state = preparedLedgerReleased
		pin := p.pin
		p.pin = nil
		p.stateMap = nil
		p.txMap = nil
		p.header = header.LedgerHeader{}
		p.mu.Unlock()
		if pin != nil {
			pin.Release()
		}
		return
	default:
		p.mu.Unlock()
		return
	}
}

// Stats returns the immutable work counters recorded while preparing the
// detached ledger.
func (p *PreparedLedger) Stats() ReplayAdmissionStats {
	if p == nil {
		return ReplayAdmissionStats{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *PreparedLedger) beginConsume(service *Service) error {
	if p == nil {
		return errReplayPreparedInvalid
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.service != service {
		return errReplayAdmissionService
	}
	if p.state != preparedLedgerReady {
		return errReplayAdmissionState
	}
	p.state = preparedLedgerConsuming
	return nil
}

func (p *PreparedLedger) finishConsume(published bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.state != preparedLedgerConsuming {
		p.mu.Unlock()
		return
	}
	releaseWork := p.releaseWork
	p.releaseWork = nil
	releasePin := !published || p.releaseAfter
	if published {
		p.state = preparedLedgerPublished
	} else {
		p.state = preparedLedgerReleased
	}
	p.stateMap = nil
	p.txMap = nil
	p.header = header.LedgerHeader{}
	var pin *replaySnapshotPin
	if releasePin {
		pin = p.pin
		p.pin = nil
	}
	p.mu.Unlock()
	if releaseWork != nil {
		releaseWork()
	}
	if pin != nil {
		pin.Release()
	}
}

func (s *Service) replaySnapshotProvider() replaySnapshotProvider {
	if provider, ok := s.shamapFamily.(replaySnapshotProvider); ok {
		return provider
	}
	if durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase); ok {
		return durable
	}
	return nil
}

func (s *Service) acquireReplaySnapshot(ctx context.Context) (*replaySnapshotPin, bool, error) {
	provider := s.replaySnapshotProvider()
	if provider == nil {
		return nil, false, nil
	}
	fingerprint, release, err := provider.AcquireDurableSnapshot(ctx)
	if err != nil {
		if release != nil {
			release()
		}
		if errors.Is(err, errors.ErrUnsupported) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if fingerprint == ([32]byte{}) {
		if release != nil {
			release()
		}
		return nil, false, nil
	}
	if release == nil {
		return nil, false, nil
	}
	return newReplaySnapshotPin(provider, fingerprint, release), true, nil
}

func (s *Service) hasDurableFamily() bool {
	if s.shamapFamily == nil {
		return false
	}
	_, ok := s.shamapFamily.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	})
	return ok
}

func currentReplayFingerprint(ctx context.Context, pin *replaySnapshotPin) ([32]byte, bool, error) {
	if pin == nil || pin.provider == nil {
		return [32]byte{}, false, nil
	}
	provider, ok := pin.provider.(replayFingerprintProvider)
	if !ok {
		return [32]byte{}, false, nil
	}
	fingerprint, err := provider.DurableFingerprint(ctx)
	if errors.Is(err, errors.ErrUnsupported) {
		return [32]byte{}, false, nil
	}
	if err == nil && fingerprint == ([32]byte{}) {
		return [32]byte{}, false, nil
	}
	return fingerprint, err == nil, err
}

func (s *Service) lookupReplayProof(h header.LedgerHeader, fingerprint [32]byte, transaction bool) bool {
	s.replayProofMu.Lock()
	for i, record := range s.replayProofs {
		if !record.matchesHeader(h, fingerprint, transaction) {
			continue
		}
		if i != 0 {
			copy(s.replayProofs[1:i+1], s.replayProofs[:i])
			s.replayProofs[0] = record
		}
		s.replayProofMu.Unlock()
		return true
	}
	s.replayProofMu.Unlock()
	if proof, ok := s.currentValidatedStateBaseProof(); ok &&
		validatedStateBaseProofMatchesLedger(proof, h, fingerprint) {
		return true
	}
	return false
}

func (s *Service) rememberReplayProof(h header.LedgerHeader, fingerprint [32]byte, transactionComplete bool) {
	if h.Hash == ([32]byte{}) || h.AccountHash == ([32]byte{}) || fingerprint == ([32]byte{}) {
		return
	}
	record := replayCompletenessRecord{
		sequence:             h.LedgerIndex,
		ledgerHash:           h.Hash,
		parentHash:           h.ParentHash,
		stateRoot:            h.AccountHash,
		txRoot:               h.TxHash,
		nodeStoreFingerprint: fingerprint,
		transactionComplete:  transactionComplete,
	}
	s.replayProofMu.Lock()
	for i := range s.replayProofs {
		if s.replayProofs[i].ledgerHash == record.ledgerHash {
			copy(s.replayProofs[i:], s.replayProofs[i+1:])
			s.replayProofs = s.replayProofs[:len(s.replayProofs)-1]
			break
		}
	}
	s.replayProofs = append([]replayCompletenessRecord{record}, s.replayProofs...)
	if len(s.replayProofs) > replayProofCacheLimit {
		s.replayProofs = s.replayProofs[:replayProofCacheLimit]
	}
	s.replayProofMu.Unlock()
}

func (s *Service) acquireReplayPublicationWork(ctx context.Context) (func(), error) {
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
	return sync.OnceFunc(func() {
		releaseMemory()
		s.ledgerWorkWG.Done()
	}), nil
}

func (s *Service) acquireReplayPreparationWork(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.lifecycleMu.Lock()
	if s.lifecycleState == serviceStopping || s.lifecycleState == serviceStopped {
		s.lifecycleMu.Unlock()
		return nil, errServiceNotRunning
	}
	s.ledgerWorkWG.Add(1)
	s.lifecycleMu.Unlock()
	return sync.OnceFunc(s.ledgerWorkWG.Done), nil
}

// PrepareReplayParent verifies a replay parent outside the service lifecycle
// locks and returns a proof that can be used for a sparse child verification.
// The proof is intentionally state-only: the replay child has a separately
// rebuilt transaction tree.
func (s *Service) PrepareReplayParent(ctx context.Context, parent *ledger.Ledger) (*PreparedReplayParent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	releasePreparation, err := s.acquireReplayPreparationWork(ctx)
	if err != nil {
		return nil, err
	}
	defer releasePreparation()
	startedAt := time.Now()
	if parent == nil {
		return nil, errors.New("replay admission: nil parent ledger")
	}
	parentHeader := parent.Header()
	snapshotStartedAt := time.Now()
	stateMap, err := parent.StateMapSnapshotContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay admission: snapshot parent state: %w", err)
	}
	if stateMap.Type() != shamap.TypeState {
		return nil, fmt.Errorf("replay admission: parent state map has type %s", stateMap.Type())
	}
	if err := stateMap.SetImmutable(); err != nil {
		return nil, fmt.Errorf("replay admission: freeze parent state snapshot: %w", err)
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("replay admission: hash parent state: %w", err)
	}
	if stateRoot != parentHeader.AccountHash {
		return nil, fmt.Errorf("replay admission: parent state root mismatch: got %x want %x", stateRoot[:8], parentHeader.AccountHash[:8])
	}

	pin, pinned, err := s.acquireReplaySnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay admission: acquire parent generation: %w", err)
	}
	reusable := pinned && stateMap.IsBacked() && s.hasDurableFamily()
	proof := &PreparedReplayParent{
		service:   s,
		header:    parentHeader,
		stateRoot: stateRoot,
		reusable:  reusable,
		pin:       pin,
	}
	if pinned {
		proof.fingerprint = pin.fingerprint
	}
	proof.stats.GenerationPinned = pinned
	proof.stats.SnapshotElapsed = time.Since(snapshotStartedAt)
	if reusable && s.lookupReplayProof(parentHeader, pin.fingerprint, false) {
		proof.stats.ProofCacheChecked = true
		proof.stats.ProofCacheHit = true
		proof.stats.Elapsed = time.Since(startedAt)
		return proof, nil
	}
	if reusable {
		proof.stats.ProofCacheChecked = true
		proof.stats.ProofCacheMiss = true
	}
	stateStats, err := s.verifyReplaySnapshot(ctx, "replay parent state", stateMap, [32]byte{})
	proof.stats.State = stateStats
	if err != nil {
		proof.Release()
		return nil, err
	}
	if reusable {
		s.rememberReplayProof(parentHeader, pin.fingerprint, false)
	}
	proof.stats.Elapsed = time.Since(startedAt)
	return proof, nil
}

func (s *Service) snapshotPreparedLedger(ctx context.Context, source *ledger.Ledger) (header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap, error) {
	if source == nil {
		return header.LedgerHeader{}, nil, nil, errors.New("replay admission: nil derived ledger")
	}
	h := source.Header()
	stateMap, err := source.StateMapSnapshotContext(ctx)
	if err != nil {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: snapshot derived state: %w", err)
	}
	if stateMap.Type() != shamap.TypeState {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: derived state map has type %s", stateMap.Type())
	}
	if err := stateMap.SetImmutable(); err != nil {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: freeze derived state snapshot: %w", err)
	}
	txMap, err := source.TxMapSnapshotContext(ctx)
	if err != nil {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: snapshot derived transactions: %w", err)
	}
	if txMap.Type() != shamap.TypeTransaction {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: derived transaction map has type %s", txMap.Type())
	}
	if err := txMap.SetImmutable(); err != nil {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: freeze derived transaction snapshot: %w", err)
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: hash derived state: %w", err)
	}
	txRoot, err := txMap.Hash()
	if err != nil {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: hash derived transactions: %w", err)
	}
	if stateRoot != h.AccountHash {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: derived state root mismatch: got %x want %x", stateRoot[:8], h.AccountHash[:8])
	}
	if txRoot != h.TxHash {
		return header.LedgerHeader{}, nil, nil, fmt.Errorf("replay admission: derived transaction root mismatch: got %x want %x", txRoot[:8], h.TxHash[:8])
	}
	return h, stateMap, txMap, nil
}

// PrepareReplayLedger performs the expensive detached verification before the
// publication lock. A non-nil parent proof enables the verified-base sparse
// walk; a nil proof safely performs the existing cold verification.
func (s *Service) PrepareReplayLedger(ctx context.Context, derived *ledger.Ledger, parent *PreparedReplayParent) (*PreparedLedger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	releasePreparation, err := s.acquireReplayPreparationWork(ctx)
	if err != nil {
		return nil, err
	}
	defer releasePreparation()
	startedAt := time.Now()
	snapshotStartedAt := time.Now()
	h, stateMap, txMap, err := s.snapshotPreparedLedger(ctx, derived)
	if err != nil {
		return nil, err
	}
	if parent != nil && parent.service != s {
		return nil, errReplayAdmissionService
	}
	if parent != nil && parent.header.Hash != h.ParentHash {
		return nil, fmt.Errorf("%w: got %x want %x", errReplayParentMismatch, h.ParentHash[:8], parent.header.Hash[:8])
	}

	pin, pinned, err := s.acquireReplaySnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay admission: acquire derived generation: %w", err)
	}
	reusable := pinned && stateMap.IsBacked() && s.hasDurableFamily()
	baseRoot, parentFingerprint, useBase := parent.baseRoot(s, h)
	if useBase && (!reusable || parentFingerprint != pin.fingerprint) {
		useBase = false
	}
	stats := ReplayAdmissionStats{
		GenerationPinned: pinned,
		ParentProofUsed:  useBase,
		SnapshotElapsed:  time.Since(snapshotStartedAt),
	}
	var stateStats ReplayAdmissionMapStats
	if useBase {
		stateStats, err = s.verifyReplaySnapshot(ctx, "replay derived state", stateMap, baseRoot)
		if err != nil {
			if pin != nil {
				pin.Release()
			}
			return nil, err
		}
	} else {
		stateStats, err = s.verifyReplaySnapshot(ctx, "replay derived state", stateMap, [32]byte{})
		if err != nil {
			if pin != nil {
				pin.Release()
			}
			return nil, err
		}
	}
	stats.State = stateStats
	txStats, err := s.verifyReplaySnapshot(ctx, "replay derived transactions", txMap, [32]byte{})
	if err != nil {
		if pin != nil {
			pin.Release()
		}
		return nil, err
	}
	stats.Transactions = txStats

	if pinned && reusable {
		s.rememberReplayProof(h, pin.fingerprint, txMap.IsBacked() && s.hasDurableFamily())
	}
	releaseWork, err := s.acquireReplayPublicationWork(ctx)
	if err != nil {
		if pin != nil {
			pin.Release()
		}
		return nil, fmt.Errorf("replay admission: acquire publication work: %w", err)
	}
	stats.Elapsed = time.Since(startedAt)
	return &PreparedLedger{
		service:  s,
		header:   h,
		stateMap: stateMap,
		txMap:    txMap,
		fingerprint: func() [32]byte {
			if pin == nil {
				return [32]byte{}
			}
			return pin.fingerprint
		}(),
		pin:         pin,
		releaseWork: releaseWork,
		stats:       stats,
	}, nil
}

func (s *Service) verifyReplayProof(
	ctx context.Context,
	h header.LedgerHeader,
	stateMap, txMap *shamap.SHAMap,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if stateMap == nil || txMap == nil || !stateMap.IsBacked() || !s.hasDurableFamily() {
		return false, nil
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		return false, fmt.Errorf("state admission: hash replay proof state: %w", err)
	}
	txRoot, err := txMap.Hash()
	if err != nil {
		return false, fmt.Errorf("state admission: hash replay proof transactions: %w", err)
	}
	if stateRoot != h.AccountHash || txRoot != h.TxHash {
		return false, nil
	}
	pin, pinned, err := s.acquireReplaySnapshot(ctx)
	if err != nil {
		return false, fmt.Errorf("state admission: acquire replay proof generation: %w", err)
	}
	if !pinned {
		return false, nil
	}
	defer pin.Release()
	if txMap.IsBacked() && s.lookupReplayProof(h, pin.fingerprint, true) {
		return true, nil
	}
	if !s.lookupReplayProof(h, pin.fingerprint, false) {
		return false, nil
	}
	if err := s.verifyDetachedSnapshot(ctx, "replay proof transactions", txMap, [32]byte{}); err != nil {
		return false, err
	}
	return true, nil
}

// BootstrapPreparedLedger publishes a prepared ledger without re-running a
// detached tree walk. Preparation owns the persistence-work admission, so
// this method must remain short and must not call acquireLedgerWork.
func (s *Service) BootstrapPreparedLedger(ctx context.Context, prepared *PreparedLedger) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if prepared == nil {
		return false, errReplayPreparedInvalid
	}
	if err := prepared.beginConsume(s); err != nil {
		return false, err
	}
	published := false
	defer func() {
		if !published {
			prepared.finishConsume(false)
		}
	}()
	prepared.mu.Lock()
	pin := prepared.pin
	fingerprint := prepared.fingerprint
	prepared.mu.Unlock()
	if pin != nil && !pin.active() {
		return false, errReplayAdmissionState
	}
	if fingerprint != ([32]byte{}) {
		current, available, err := currentReplayFingerprint(ctx, pin)
		if err != nil {
			return false, fmt.Errorf("replay admission: recheck publication generation: %w", err)
		}
		if available && current != fingerprint {
			prepared.mu.Lock()
			prepared.stats.GenerationChanged = true
			prepared.mu.Unlock()
			return false, errors.New("replay admission: publication generation changed")
		}
	}

	var initialCandidate bool
	var err error
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.historyComponent.mu.Lock()
		defer s.historyComponent.mu.Unlock()
		initialCandidate = s.networkLedgerState != networkLedgerReady
		err = s.storeLedgerWithStateLocked(ctx, &prepared.header, prepared.stateMap, prepared.txMap)
		if err == nil && initialCandidate && !s.ReplayBlocked() {
			s.rememberValidatedStateBaseCandidate(prepared.header)
		}
	}()
	if err == nil {
		s.rememberReplayProof(prepared.header, fingerprint,
			prepared.stateMap.IsBacked() && prepared.txMap.IsBacked() && s.hasDurableFamily())
	}
	prepared.finishConsume(err == nil)
	published = err == nil
	return initialCandidate, err
}
