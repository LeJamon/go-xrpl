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

// AcquireStateAdmission pins the current durable generation before detached
// ledger work takes a service lifecycle lock. The returned release function is
// safe to call more than once. Nested callers acquire their own reference;
// nodestore shares references for an already admitted generation.
func (s *Service) AcquireStateAdmission(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.nodeStore == nil {
		return func() {}, nil
	}
	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return func() {}, nil
	}
	_, release, err := durable.AcquireDurableSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return release, nil
}

// VerifyDetachedMaps proves that externally supplied maps have no missing
// descendants in the currently pinned generation. It must run before service
// lifecycle locks are acquired. Nil maps are valid for empty transaction trees.
func (s *Service) VerifyDetachedMaps(ctx context.Context, stateMap, txMap *shamap.SHAMap) error {
	if err := s.verifyDetachedMap(ctx, "state", stateMap); err != nil {
		return err
	}
	return s.verifyDetachedMap(ctx, "transaction", txMap)
}

// VerifyDetachedLedger verifies both trees of an externally supplied ledger.
// Ledger snapshots flush dirty backed nodes before the detached completeness
// proof, while leaving the caller's map ownership unchanged.
func (s *Service) VerifyDetachedLedger(ctx context.Context, l *ledger.Ledger) error {
	_, err := s.verifyDetachedLedgerStats(ctx, l)
	return err
}

func (s *Service) verifyDetachedLedgerStats(ctx context.Context, l *ledger.Ledger) (stats ReplayAdmissionStats, err error) {
	startedAt := time.Now()
	defer func() { stats.Elapsed = time.Since(startedAt) }()
	if l == nil {
		return stats, errors.New("state admission: nil ledger")
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	snapshotStartedAt := time.Now()
	stateMap, err := l.StateMapSnapshotContext(ctx)
	if err != nil {
		return stats, fmt.Errorf("state admission: snapshot state map: %w", err)
	}
	txMap, err := l.TxMapSnapshotContext(ctx)
	if err != nil {
		return stats, fmt.Errorf("state admission: snapshot transaction map: %w", err)
	}
	stats.SnapshotElapsed = time.Since(snapshotStartedAt)
	h := l.Header()
	if verified, err := s.verifyReplayProof(ctx, h, stateMap, txMap); err != nil {
		return stats, err
	} else if verified {
		stats.ProofCacheChecked = true
		stats.ProofCacheHit = true
		return stats, nil
	}
	stats.ProofCacheChecked = true
	stats.ProofCacheMiss = true
	if !stateMap.IsBacked() || !s.hasDurableFamily() {
		s.logReplayVerificationDecision(h, [32]byte{}, "miss", "durable generation pin unavailable", false)
		stats.State, err = s.verifyDetachedMapStats(ctx, "state", stateMap)
		if err != nil {
			return stats, err
		}
		stats.Transactions, err = s.verifyDetachedMapStats(ctx, "transaction", txMap)
		return stats, err
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		return stats, fmt.Errorf("state admission: hash state map: %w", err)
	}
	if stateRoot != h.AccountHash {
		return stats, fmt.Errorf("state admission: state root mismatch: got %x want %x", stateRoot[:8], h.AccountHash[:8])
	}
	txRoot, err := txMap.Hash()
	if err != nil {
		return stats, fmt.Errorf("state admission: hash transaction map: %w", err)
	}
	if txRoot != h.TxHash {
		return stats, fmt.Errorf("state admission: transaction root mismatch: got %x want %x", txRoot[:8], h.TxHash[:8])
	}
	pin, pinned, err := s.acquireReplaySnapshot(ctx)
	if err != nil {
		return stats, fmt.Errorf("state admission: acquire replay generation: %w", err)
	}
	if !pinned {
		s.logReplayVerificationDecision(h, [32]byte{}, "miss", "durable generation pin unavailable", false)
		stats.State, err = s.verifyDetachedMapStats(ctx, "state", stateMap)
		if err != nil {
			return stats, err
		}
		stats.Transactions, err = s.verifyDetachedMapStats(ctx, "transaction", txMap)
		return stats, err
	}
	stats.GenerationPinned = true
	s.logReplayVerificationDecision(h, pin.fingerprint, "miss", s.replayVerificationMissReason(h, pin.fingerprint, txMap.IsBacked()), true)
	key := replayVerificationKey{
		sequence:             h.LedgerIndex,
		ledgerHash:           h.Hash,
		parentHash:           h.ParentHash,
		stateRoot:            h.AccountHash,
		txRoot:               h.TxHash,
		transactionBacked:    txMap.IsBacked(),
		nodeStoreFingerprint: pin.fingerprint,
	}
	result, err := s.coalesceReplayVerification(ctx, key, pin, func(ownerCtx context.Context) (replayVerificationResult, error) {
		if verified, err := s.verifyReplayProof(ownerCtx, h, stateMap, txMap); err != nil {
			return replayVerificationResult{}, err
		} else if verified {
			return replayVerificationResult{proofCacheHit: true}, nil
		}
		stateStats, stateProofReused, runErr := s.verifyDetachedLedgerState(ownerCtx, h, stateMap, pin)
		if runErr != nil {
			return replayVerificationResult{state: stateStats}, runErr
		}
		if stateProofReused {
			s.logReplayVerificationDecision(h, pin.fingerprint, "state_reused", "generation-bound state proof available for transaction upgrade", true)
		}
		txStats, runErr := s.verifyDetachedMapStats(ownerCtx, "transaction", txMap)
		if runErr != nil {
			s.logReplayVerificationDecision(h, pin.fingerprint, "state_only", "state traversal complete; transaction verification failed", true)
			return replayVerificationResult{state: stateStats, transactions: txStats}, runErr
		}
		current, available, runErr := currentReplayFingerprint(ownerCtx, pin)
		if runErr != nil {
			return replayVerificationResult{state: stateStats, transactions: txStats}, fmt.Errorf("state admission: recheck replay generation: %w", runErr)
		}
		if available && current != pin.fingerprint {
			return replayVerificationResult{state: stateStats, transactions: txStats}, errors.New("state admission: replay generation changed")
		}
		if err := ownerCtx.Err(); err != nil {
			return replayVerificationResult{state: stateStats, transactions: txStats}, err
		}
		if txMap.IsBacked() {
			s.rememberReplayProof(h, pin.fingerprint, true)
		}
		s.logReplayVerificationDecision(h, pin.fingerprint, "published", "state and transaction traversal complete", true)
		return replayVerificationResult{state: stateStats, transactions: txStats, proofCacheHit: stateProofReused}, nil
	})
	stats.State = result.state
	stats.Transactions = result.transactions
	if result.proofCacheHit {
		stats.ProofCacheHit = true
		stats.ProofCacheMiss = false
	}
	return stats, err
}

func (s *Service) verifyDetachedLedgerState(ctx context.Context, h header.LedgerHeader, source *shamap.SHAMap, pin *replaySnapshotPin) (stats ReplayAdmissionMapStats, reused bool, err error) {
	if err := ctx.Err(); err != nil {
		return stats, false, err
	}
	if s.lookupReplayProof(h, pin.fingerprint, false) {
		return stats, true, nil
	}
	startedAt := time.Now()
	defer func() { stats.Elapsed = time.Since(startedAt) }()
	release, err := s.acquireForegroundStateVerification(ctx)
	stats.AdmissionWait = time.Since(startedAt)
	if err != nil {
		return stats, false, err
	}
	defer release()
	// Different transaction backing uses separate flights but the same state
	// proof. Recheck and publish while holding the traversal permit.
	if s.lookupReplayProof(h, pin.fingerprint, false) {
		return stats, true, nil
	}
	if h.AccountHash != ([32]byte{}) {
		stats, err = s.walkStoredReplaySnapshot(ctx, "state", h.AccountHash, source.Type(), startedAt)
	} else {
		snapshot, snapshotErr := source.SnapshotImmutableContext(ctx)
		if snapshotErr != nil {
			return stats, false, fmt.Errorf("state admission: snapshot state map: %w", snapshotErr)
		}
		admissionWait := stats.AdmissionWait
		stats, err = s.verifyDetachedSnapshotStats(ctx, "state", snapshot, [32]byte{})
		stats.AdmissionWait = admissionWait
	}
	if err != nil {
		return stats, false, err
	}
	current, available, err := currentReplayFingerprint(ctx, pin)
	if err != nil {
		return stats, false, fmt.Errorf("state admission: recheck replay generation: %w", err)
	}
	if available && current != pin.fingerprint {
		return stats, false, errors.New("state admission: replay generation changed")
	}
	if err := ctx.Err(); err != nil {
		return stats, false, err
	}
	s.rememberReplayProof(h, pin.fingerprint, false)
	return stats, false, nil
}

func (s *Service) isServiceOwnedLedger(l *ledger.Ledger) bool {
	if l == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return l == s.closedLedger || l == s.openLedger || l == s.validatedLedger
}

func (s *Service) verifyDetachedMap(ctx context.Context, name string, source *shamap.SHAMap) error {
	_, err := s.verifyDetachedMapStats(ctx, name, source)
	return err
}

func (s *Service) verifyDetachedMapStats(ctx context.Context, name string, source *shamap.SHAMap) (ReplayAdmissionMapStats, error) {
	if source == nil {
		return ReplayAdmissionMapStats{}, nil
	}
	if err := ctx.Err(); err != nil {
		return ReplayAdmissionMapStats{}, err
	}
	// StartSync changes map state. Always make that change on a detached
	// snapshot so admission never converts the caller's source map to syncing.
	snapshot, err := source.SnapshotImmutableContext(ctx)
	if err != nil {
		return ReplayAdmissionMapStats{}, fmt.Errorf("state admission: snapshot %s map: %w", name, err)
	}
	return s.verifyReplaySnapshot(ctx, name, snapshot, [32]byte{})
}

// verifyDetachedSnapshot verifies a snapshot without changing the caller's
// map state. When baseRoot is present, equal subtrees are skipped only after
// the caller has independently established a generation-bound complete base.
// Any base-loading failure falls back to a strict walk of the target tree.
func (s *Service) verifyDetachedSnapshot(ctx context.Context, name string, snapshot *shamap.SHAMap, baseRoot [32]byte) error {
	_, err := s.verifyReplaySnapshot(ctx, name, snapshot, baseRoot)
	return err
}

func (s *Service) verifyDetachedSnapshotStats(ctx context.Context, name string, snapshot *shamap.SHAMap, baseRoot [32]byte) (stats ReplayAdmissionMapStats, err error) {
	if snapshot == nil {
		return stats, nil
	}
	startedAt := time.Now()
	defer func() {
		stats.Elapsed = time.Since(startedAt)
		stats.TraversalElapsed = stats.Elapsed
	}()
	mapToVerify, err := s.coldDetachedMap(ctx, snapshot)
	if err != nil {
		return stats, fmt.Errorf("state admission: load %s root: %w", name, err)
	}
	before := mapToVerify.BackedWalkStats()
	usedVerifiedBase := false
	if baseRoot != ([32]byte{}) {
		if err := mapToVerify.SetVerifiedBaseContext(ctx, baseRoot); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return stats, ctxErr
			}
			// A missing or expired base is an optimization failure. The target
			// still receives a complete cold walk below.
		} else {
			usedVerifiedBase = true
		}
	}
	if err := mapToVerify.StartSync(); err != nil {
		return stats, fmt.Errorf("state admission: start %s completeness check: %w", name, err)
	}
	missing, err := mapToVerify.GetMissingNodesContext(ctx, 1, nil)
	if err != nil {
		return stats, fmt.Errorf("state admission: walk %s tree: %w", name, err)
	}
	if len(missing) != 0 {
		m := missing[0]
		return stats, fmt.Errorf("state admission: incomplete %s tree at depth %d: %w", name, m.Depth, &shamap.MissingNodeError{Hash: m.Hash})
	}
	if err := mapToVerify.FinishSyncContext(ctx); err != nil {
		return stats, fmt.Errorf("state admission: finish %s completeness check: %w", name, err)
	}
	after := mapToVerify.BackedWalkStats()
	stats.NodesLoaded = after.TraversalDiscoveries - before.TraversalDiscoveries
	stats.DurableReads = after.DurableReads - before.DurableReads
	stats.EqualSubtreesSkipped = after.EqualSubtreesSkipped - before.EqualSubtreesSkipped
	stats.UsedVerifiedBase = usedVerifiedBase
	stats.Workers = 1
	return stats, nil
}

// coldDetachedMap removes attached warm descendants before verification. A
// warm alias can otherwise satisfy a missing-node walk from memory after its
// durable backing was reclaimed. Durable fetches are used for the fresh root
// and every descendant when the configured family exposes that capability.
func (s *Service) coldDetachedMap(ctx context.Context, source *shamap.SHAMap) (*shamap.SHAMap, error) {
	if !source.IsBacked() || s.shamapFamily == nil {
		return source, nil
	}
	root, err := source.Hash()
	if err != nil {
		return nil, err
	}
	if root == ([32]byte{}) {
		return source, nil
	}
	family := durableAdmissionFamily{Family: s.shamapFamily}
	durable, ok := s.shamapFamily.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	})
	if !ok {
		return source, nil
	}
	family.durable = durable
	if snapshot, ok := s.shamapFamily.(interface {
		AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	}); ok {
		family.snapshot = snapshot
	}
	if provider, ok := s.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	}); ok {
		family.cache = provider.FullBelowCache()
	}
	return shamap.NewFromRootHashContext(ctx, source.Type(), root, family)
}

type durableAdmissionFamily struct {
	shamap.Family
	durable interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}
	snapshot interface {
		AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	}
	cache *shamap.FullBelowCache
}

func (f durableAdmissionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.durable.FetchDurable(ctx, hash)
}

func (f durableAdmissionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.durable.FetchDurable(ctx, hash)
}

func (f durableAdmissionFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	if f.snapshot == nil {
		return [32]byte{}, nil, errors.ErrUnsupported
	}
	return f.snapshot.AcquireDurableSnapshot(ctx)
}

func (f durableAdmissionFamily) FullBelowCache() *shamap.FullBelowCache {
	return f.cache
}
