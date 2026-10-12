package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

const (
	replayProofCacheLimit        = 4
	replayPreparedProofSlotLimit = 8
)

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

func (r replayCompletenessRecord) matchesExactHeader(h header.LedgerHeader, fingerprint [32]byte, transaction bool) bool {
	return r.matchesHeader(h, fingerprint, transaction) && r.txRoot == h.TxHash
}

// Persistence owns a copy, independent of the prepared token's lifetime and
// the small replay LRU. This is not publication: the durable generation,
// header, roots and canonical tip must still be checked after persistence.
func (s *Service) replayPersistenceProof(h header.LedgerHeader) *replayCompletenessRecord {
	s.replayProofMu.Lock()
	defer s.replayProofMu.Unlock()
	usable := func(record replayCompletenessRecord) bool {
		return record.nodeStoreFingerprint != ([32]byte{}) &&
			record.matchesExactHeader(h, record.nodeStoreFingerprint, h.TxHash != ([32]byte{}))
	}
	for _, proof := range s.replayPreparedProofs {
		if proof.pin != nil && proof.pin.active() && proof.record.nodeStoreFingerprint != ([32]byte{}) &&
			proof.record.matchesExactHeader(h, proof.record.nodeStoreFingerprint, false) {
			record := proof.record
			// Preparation verified both immutable maps, including an in-memory
			// transaction tree. The persistence job writes that tree before
			// this certificate can be promoted; the ordinary replay LRU must
			// not treat a state-only record as a transaction proof.
			record.transactionComplete = true
			return &record
		}
	}
	for _, record := range s.replayProofs {
		if usable(record) {
			return &record
		}
	}
	return nil
}

// replayPreparedProof is the service-owned activation certificate for a
// prepared result. It deliberately retains no map or node payload; the token
// owns those resources and remains responsible for releasing the generation
// pin and slot.
type replayPreparedProof struct {
	owner       *PreparedLedger
	record      replayCompletenessRecord
	pin         *replaySnapshotPin
	releaseSlot func()
	published   bool
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
// It owns immutable map snapshots, a durable pin, and the persistence and
// lifecycle admissions acquired before the caller enters its commit lock.
type PreparedLedger struct {
	service          *Service
	header           header.LedgerHeader
	ledger           *ledger.Ledger
	stateMap         *shamap.SHAMap
	txMap            *shamap.SHAMap
	fingerprint      [32]byte
	pin              *replaySnapshotPin
	releaseWork      func()
	releaseLifecycle func()
	stats            ReplayAdmissionStats
	state            preparedLedgerState
	releaseAfter     bool
	mu               sync.Mutex
}

type preparedLedgerState uint8

const (
	preparedLedgerReady preparedLedgerState = iota
	preparedLedgerConsuming
	preparedLedgerPublished
	preparedLedgerReleased
)

// Release releases all resources owned by a prepared ledger. A successfully
// published token keeps its durable snapshot pin and lifecycle admission until
// this method is called, allowing the caller to cover the subsequent
// activation step.
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
		releaseLifecycle := p.releaseLifecycle
		p.releaseWork = nil
		p.releaseLifecycle = nil
		p.pin = nil
		p.ledger = nil
		p.stateMap = nil
		p.txMap = nil
		p.header = header.LedgerHeader{}
		service := p.service
		p.mu.Unlock()
		if service != nil {
			service.unregisterReplayPrepared(p)
		}
		if releaseWork != nil {
			releaseWork()
		}
		if pin != nil {
			pin.Release()
		}
		if releaseLifecycle != nil {
			releaseLifecycle()
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
		p.ledger = nil
		p.stateMap = nil
		p.txMap = nil
		p.header = header.LedgerHeader{}
		releaseLifecycle := p.releaseLifecycle
		p.releaseLifecycle = nil
		service := p.service
		p.mu.Unlock()
		if service != nil {
			service.unregisterReplayPrepared(p)
		}
		if pin != nil {
			pin.Release()
		}
		if releaseLifecycle != nil {
			releaseLifecycle()
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
	releaseAfter := p.releaseAfter
	releasePin := !published || releaseAfter
	releaseLifecycle := p.releaseLifecycle
	if !published || releaseAfter {
		p.releaseLifecycle = nil
	}
	service := p.service
	if published && !releaseAfter {
		p.state = preparedLedgerPublished
	} else {
		p.state = preparedLedgerReleased
	}
	p.ledger = nil
	p.stateMap = nil
	p.txMap = nil
	p.header = header.LedgerHeader{}
	var pin *replaySnapshotPin
	if releasePin {
		pin = p.pin
		p.pin = nil
	}
	p.mu.Unlock()
	if service != nil {
		if published && !releasePin {
			service.markReplayPreparedPublished(p)
		} else {
			service.unregisterReplayPrepared(p)
		}
	}
	if releaseWork != nil {
		releaseWork()
	}
	if pin != nil {
		pin.Release()
	}
	if releaseLifecycle != nil && (!published || releaseAfter) {
		releaseLifecycle()
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

func (s *Service) acquireReplayPreparedSlot(ctx context.Context) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.lifecycleMu.Lock()
		if s.lifecycleState == serviceStopping || s.lifecycleState == serviceStopped {
			s.lifecycleMu.Unlock()
			return nil, errServiceNotRunning
		}
		s.replayProofMu.Lock()
		if s.replayPreparedSlotCount < replayPreparedProofSlotLimit {
			s.replayPreparedSlotCount++
			s.replayProofMu.Unlock()
			s.lifecycleMu.Unlock()
			return sync.OnceFunc(s.releaseReplayPreparedSlot), nil
		}
		if s.replayPreparedProofWake == nil {
			s.replayPreparedProofWake = make(chan struct{})
		}
		wake := s.replayPreparedProofWake
		s.replayProofMu.Unlock()
		s.lifecycleMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (s *Service) releaseReplayPreparedSlot() {
	s.replayProofMu.Lock()
	if s.replayPreparedSlotCount > 0 {
		s.replayPreparedSlotCount--
	}
	wake := s.replayPreparedProofWake
	if wake != nil {
		s.replayPreparedProofWake = make(chan struct{})
	}
	s.replayProofMu.Unlock()
	if wake != nil {
		close(wake)
	}
}

func (s *Service) signalReplayPreparedSlots() {
	s.replayProofMu.Lock()
	wake := s.replayPreparedProofWake
	s.replayPreparedProofWake = nil
	s.replayProofMu.Unlock()
	if wake != nil {
		close(wake)
	}
}

func (s *Service) registerReplayPrepared(
	prepared *PreparedLedger,
	record replayCompletenessRecord,
	pin *replaySnapshotPin,
	releaseSlot func(),
) error {
	s.lifecycleMu.Lock()
	if s.lifecycleState == serviceStopping || s.lifecycleState == serviceStopped {
		s.lifecycleMu.Unlock()
		return errServiceNotRunning
	}
	s.replayProofMu.Lock()
	s.replayPreparedProofs = append(s.replayPreparedProofs, &replayPreparedProof{
		owner:       prepared,
		record:      record,
		pin:         pin,
		releaseSlot: releaseSlot,
	})
	s.replayProofMu.Unlock()
	s.lifecycleMu.Unlock()
	return nil
}

func (s *Service) unregisterReplayPrepared(prepared *PreparedLedger) {
	if prepared == nil {
		return
	}
	var releaseSlot func()
	s.replayProofMu.Lock()
	for i, proof := range s.replayPreparedProofs {
		if proof.owner != prepared {
			continue
		}
		releaseSlot = proof.releaseSlot
		copy(s.replayPreparedProofs[i:], s.replayPreparedProofs[i+1:])
		s.replayPreparedProofs[len(s.replayPreparedProofs)-1] = nil
		s.replayPreparedProofs = s.replayPreparedProofs[:len(s.replayPreparedProofs)-1]
		break
	}
	s.replayProofMu.Unlock()
	if releaseSlot != nil {
		releaseSlot()
	}
}

func (s *Service) markReplayPreparedPublished(prepared *PreparedLedger) {
	if prepared == nil {
		return
	}
	s.replayProofMu.Lock()
	for _, proof := range s.replayPreparedProofs {
		if proof.owner == prepared {
			proof.published = true
			break
		}
	}
	s.replayProofMu.Unlock()
}

func (s *Service) lookupReplayProof(h header.LedgerHeader, fingerprint [32]byte, transaction bool) bool {
	s.replayProofMu.Lock()
	for _, proof := range s.replayPreparedProofs {
		if proof.published && proof.pin != nil && proof.pin.active() &&
			proof.record.matchesExactHeader(h, fingerprint, transaction) {
			s.replayProofMu.Unlock()
			return true
		}
	}
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
	stateStats, refreshedProof, err := s.verifyReplayParentWithRefresh(ctx, parentHeader, stateMap, proof)
	proof.stats.State = stateStats
	if err != nil {
		proof.Release()
		return nil, err
	}
	if reusable {
		s.rememberReplayProof(parentHeader, pin.fingerprint, false)
	}
	if refreshedProof {
		proof.stats.ProofCacheHit = true
	}
	proof.stats.Elapsed = time.Since(startedAt)
	return proof, nil
}

var errReplayParentProofAvailable = errors.New("replay parent completeness proof became available")

// Persistence or background re-certification can certify this exact parent
// while a cold scan is running. Recheck the generation-bound proof instead of
// finishing millions of redundant reads. No proof means no early admission.
func (s *Service) verifyReplayParentWithRefresh(ctx context.Context, h header.LedgerHeader, stateMap *shamap.SHAMap, proof *PreparedReplayParent) (ReplayAdmissionMapStats, bool, error) {
	if !proof.reusable {
		stats, err := s.verifyReplaySnapshot(ctx, "replay parent state", stateMap, [32]byte{})
		return stats, false, err
	}
	walkCtx, cancel := context.WithCancelCause(ctx)
	var baseRoot [32]byte
	if base, ok := s.currentValidatedStateBaseProof(); ok && base.nodeStoreFingerprint == proof.fingerprint {
		// Completeness is a property of a Merkle root, not adjacency. A
		// certified older state also safely skips equal parent subtrees.
		baseRoot = base.stateRoot
	}
	if s.logger != nil {
		s.logger.Info("Replay parent verification selected", "sequence", h.LedgerIndex,
			"verified_base", baseRoot != ([32]byte{}), "reason", "exact completeness proof unavailable")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-walkCtx.Done():
				return
			case <-ticker.C:
				if s.lookupReplayProof(h, proof.fingerprint, false) {
					cancel(errReplayParentProofAvailable)
					return
				}
			}
		}
	}()
	stats, err := s.verifyReplaySnapshot(walkCtx, "replay parent state", stateMap, baseRoot)
	cancel(nil)
	<-done
	if (errors.Is(err, context.Canceled) || errors.Is(err, errReplayParentProofAvailable)) && ctx.Err() == nil &&
		errors.Is(context.Cause(walkCtx), errReplayParentProofAvailable) &&
		s.lookupReplayProof(h, proof.fingerprint, false) {
		return stats, true, nil
	}
	return stats, false, err
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
	return s.prepareLedger(ctx, derived, parent, false, nil)
}

// Acquisition may compare against a nonconsecutive checkpoint/validated base.
// Keep that choice separate from replay's strict parent-identity requirement.
func (s *Service) prepareLedger(ctx context.Context, derived *ledger.Ledger, parent *PreparedReplayParent, acquired bool, pin *replaySnapshotPin) (*PreparedLedger, error) {
	pinOwned := true
	defer func() {
		if pinOwned && pin != nil {
			pin.Release()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	releasePreparedSlot, err := s.acquireReplayPreparedSlot(ctx)
	if err != nil {
		return nil, err
	}
	preparedSlotOwned := true
	defer func() {
		if preparedSlotOwned {
			releasePreparedSlot()
		}
	}()
	releasePreparation, err := s.acquireReplayPreparationWork(ctx)
	if err != nil {
		return nil, err
	}
	preparationOwned := true
	defer func() {
		if preparationOwned {
			releasePreparation()
		}
	}()
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

	pinned := pin != nil
	if !pinned {
		pin, pinned, err = s.acquireReplaySnapshot(ctx)
		if err != nil {
			return nil, fmt.Errorf("replay admission: acquire derived generation: %w", err)
		}
	}
	reusable := pinned && stateMap.IsBacked() && s.hasDurableFamily()
	baseRoot, parentFingerprint, useBase := parent.baseRoot(s, h)
	parentProofAvailable := useBase
	stats := ReplayAdmissionStats{
		GenerationPinned: pinned,
		SnapshotElapsed:  time.Since(snapshotStartedAt),
	}
	if parentProofAvailable {
		generationChanged := pinned && pin != nil && parentFingerprint != pin.fingerprint
		if generationChanged {
			stats.GenerationChanged = true
		}
		useBase = reusable && !generationChanged
	}
	stats.ParentProofUsed = useBase
	if !useBase {
		baseRoot = [32]byte{}
	}
	statePurpose := "replay derived state"
	if acquired {
		statePurpose = "acquired state"
		var baseSequence uint32
		if reusable {
			// Read the service-owned certificate only after pinning this
			// generation. Never trust acquisition's map flags or an unpinned
			// root supplied by its caller. The certificate proves the entire
			// base tree, independently of shallow FullBelow cache residency.
			if proof, ok := s.currentValidatedStateBaseProof(); ok {
				if proof.nodeStoreFingerprint == pin.fingerprint {
					baseRoot, baseSequence = proof.stateRoot, proof.sequence
				} else {
					stats.GenerationChanged = true
				}
			}
		}
		s.logger.Info("Acquired ledger verification selected",
			"sequence", h.LedgerIndex, "base_sequence", baseSequence,
			"verified_base", baseRoot != ([32]byte{}), "generation_changed", stats.GenerationChanged)
	}
	immutable, err := derived.Snapshot()
	if err == nil && immutable.ExecutionStore() != s.config.Execution {
		immutable, err = ledger.NewClosedFromRootsContext(ctx, h, derived.Fees(), s.config.Execution)
	}
	if err != nil {
		return nil, err
	}
	err = immutable.AuthenticateClosureWith(ctx, func(ctx context.Context, verified header.LedgerHeader) error {
		if verified.Hash != h.Hash {
			return errReplayPreparedInvalid
		}
		var err error
		stats.State, err = s.verifyReplaySnapshot(ctx, statePurpose, stateMap, baseRoot)
		if err != nil {
			return err
		}
		stats.Transactions, err = s.verifyReplaySnapshot(ctx, "replay derived transactions", txMap, [32]byte{})
		return err
	})
	if err != nil {
		return nil, err
	}

	if pinned && reusable {
		s.rememberReplayProof(h, pin.fingerprint, txMap.IsBacked() && s.hasDurableFamily())
	}
	releaseWork, err := s.acquireReplayPublicationWork(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay admission: acquire publication work: %w", err)
	}
	stats.Elapsed = time.Since(startedAt)
	fingerprint := [32]byte{}
	if pin != nil {
		fingerprint = pin.fingerprint
	}
	prepared := &PreparedLedger{
		ledger:           immutable,
		service:          s,
		header:           h,
		stateMap:         stateMap,
		txMap:            txMap,
		fingerprint:      fingerprint,
		pin:              pin,
		releaseWork:      releaseWork,
		releaseLifecycle: releasePreparation,
		stats:            stats,
	}
	preparedRecord := replayCompletenessRecord{
		sequence:             h.LedgerIndex,
		ledgerHash:           h.Hash,
		parentHash:           h.ParentHash,
		stateRoot:            h.AccountHash,
		txRoot:               h.TxHash,
		nodeStoreFingerprint: fingerprint,
		transactionComplete:  txMap.IsBacked() && s.hasDurableFamily(),
	}
	if err := s.registerReplayPrepared(prepared, preparedRecord, pin, releasePreparedSlot); err != nil {
		releaseWork()
		return nil, fmt.Errorf("replay admission: retain prepared proof: %w", err)
	}
	pinOwned = false
	preparedSlotOwned = false
	preparationOwned = false
	return prepared, nil
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

// PrepareAcquiredLedger binds a complete acquired state to its header and
// durable generation. It compares against a service-certified complete base
// from the same generation when available, otherwise falling back to a full
// cold walk. It must run on a worker, never on the consensus router.
func (s *Service) PrepareAcquiredLedger(ctx context.Context, h *header.LedgerHeader, stateMap, txMap *shamap.SHAMap) (*PreparedLedger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	releasePreparation, err := s.acquireReplayPreparationWork(ctx)
	if err != nil {
		return nil, err
	}
	defer releasePreparation()
	if h == nil || stateMap == nil {
		return nil, errors.New("acquired ledger requires a header and state map")
	}
	if header.CalculateHash(*h) != h.Hash {
		return nil, errors.New("acquired ledger header hash mismatch")
	}
	if txMap == nil {
		if h.TxHash != ([32]byte{}) {
			return nil, errors.New("nil transaction map for non-empty transaction root")
		}
		txMap = shamap.New(shamap.TypeTransaction)
	}
	pin, _, err := s.acquireReplaySnapshot(ctx)
	if err != nil {
		return nil, err
	}
	pinOwned := true
	defer func() {
		if pinOwned && pin != nil {
			pin.Release()
		}
	}()
	stateRoot, txRoot, err := s.config.Execution.ImportMaps(ctx, stateMap, txMap, h.LedgerIndex)
	if err != nil {
		return nil, err
	}
	if stateRoot != h.AccountHash || txRoot != h.TxHash {
		return nil, errors.New("acquired ledger roots do not match header")
	}
	source, err := ledger.NewClosedFromRootsContext(ctx, *h, drops.Fees{}, s.config.Execution)
	if err != nil {
		return nil, err
	}
	pinOwned = false
	return s.prepareLedger(ctx, source, nil, true, pin)
}

// BootstrapPreparedLedger publishes a prepared ledger without re-running a
// detached tree walk. Preparation owns the persistence-work admission, so
// this method must remain short and must not call acquireLedgerWork.
func (s *Service) BootstrapPreparedLedger(ctx context.Context, prepared *PreparedLedger) (bool, error) {
	return s.publishPreparedLedger(ctx, prepared, preparedAcquisitionConsensus)
}

// StorePreparedLedger stores a verified acquisition without moving consensus.
func (s *Service) StorePreparedLedger(ctx context.Context, prepared *PreparedLedger) error {
	_, err := s.publishPreparedLedger(ctx, prepared, preparedAcquisitionGeneric)
	return err
}

// IngestHistoricalPreparedLedger retains the canonical-history checks while
// reusing the worker's completeness proof instead of walking the state again.
func (s *Service) IngestHistoricalPreparedLedger(ctx context.Context, prepared *PreparedLedger) error {
	_, err := s.publishPreparedLedger(ctx, prepared, preparedAcquisitionHistory)
	return err
}

type preparedAcquisitionMode uint8

const (
	preparedAcquisitionConsensus preparedAcquisitionMode = iota
	preparedAcquisitionGeneric
	preparedAcquisitionHistory
)

func (s *Service) publishPreparedLedger(ctx context.Context, prepared *PreparedLedger, mode preparedAcquisitionMode) (bool, error) {
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
		if mode == preparedAcquisitionHistory {
			err = s.ingestHistoricalLedgerLocked(ctx, prepared.ledger)
			return
		}
		err = s.storeLedgerLocked(ctx, prepared.ledger)
		if err == nil && (initialCandidate || mode == preparedAcquisitionGeneric) && !s.ReplayBlocked() {
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
