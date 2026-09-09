package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// validatedStateBaseProof is an in-memory completeness certificate for one
// validated ledger. It is created after a full durable startup walk (or a
// checkpoint derived from one), or promoted from a fully fetched initial-sync
// candidate after persistence. It is then inherited by consecutive persisted
// ledgers while the NodeStore generation remains unchanged. The generation
// check makes managed online deletion invalidate the certificate.
type validatedStateBaseProof struct {
	sequence             uint32
	ledgerHash           [32]byte
	parentHash           [32]byte
	stateRoot            [32]byte
	txRoot               [32]byte
	nodeStoreFingerprint [32]byte
}

func (s *Service) rememberValidatedStateBase(h header.LedgerHeader, fingerprint [32]byte) {
	proof, ok := newValidatedStateBaseProof(h, fingerprint)
	if !ok {
		return
	}
	s.validatedStateBaseMu.Lock()
	s.validatedStateBaseProof = &proof
	if candidate := s.validatedStateBaseCandidate; candidate != nil && candidate.sequence <= proof.sequence {
		s.validatedStateBaseCandidate = nil
	}
	s.validatedStateBaseMu.Unlock()
}

func newValidatedStateBaseProof(h header.LedgerHeader, fingerprint [32]byte) (validatedStateBaseProof, bool) {
	if h.LedgerIndex == 0 || h.Hash == ([32]byte{}) || h.AccountHash == ([32]byte{}) ||
		fingerprint == ([32]byte{}) {
		return validatedStateBaseProof{}, false
	}
	proof, ok := newValidatedStateBaseIdentity(h)
	if !ok {
		return validatedStateBaseProof{}, false
	}
	proof.nodeStoreFingerprint = fingerprint
	return proof, true
}

func newValidatedStateBaseIdentity(h header.LedgerHeader) (validatedStateBaseProof, bool) {
	if h.LedgerIndex == 0 || h.Hash == ([32]byte{}) || h.AccountHash == ([32]byte{}) {
		return validatedStateBaseProof{}, false
	}
	return validatedStateBaseProof{
		sequence:   h.LedgerIndex,
		ledgerHash: h.Hash,
		parentHash: h.ParentHash,
		stateRoot:  h.AccountHash,
		txRoot:     h.TxHash,
	}, true
}

// rememberValidatedStateBaseCandidate records a fully fetched initial-sync
// ledger before its asynchronous persistence completes. advanceValidated...
// promotes it only when the shared full-below cache proves both durable trees
// complete under the pinned generation.
func (s *Service) rememberValidatedStateBaseCandidate(h header.LedgerHeader) {
	proof, ok := newValidatedStateBaseIdentity(h)
	if !ok {
		return
	}
	s.validatedStateBaseMu.Lock()
	s.validatedStateBaseCandidate = &proof
	s.validatedStateBaseMu.Unlock()
}

func (s *Service) currentValidatedStateBaseProof() (validatedStateBaseProof, bool) {
	s.validatedStateBaseMu.RLock()
	defer s.validatedStateBaseMu.RUnlock()
	if s.validatedStateBaseProof == nil {
		return validatedStateBaseProof{}, false
	}
	return *s.validatedStateBaseProof, true
}

func (s *Service) currentValidatedStateBaseCandidate() (validatedStateBaseProof, bool) {
	s.validatedStateBaseMu.RLock()
	defer s.validatedStateBaseMu.RUnlock()
	if s.validatedStateBaseCandidate == nil {
		return validatedStateBaseProof{}, false
	}
	return *s.validatedStateBaseCandidate, true
}

func (s *Service) clearValidatedStateBase() {
	s.validatedStateBaseMu.Lock()
	s.validatedStateBaseProof = nil
	s.validatedStateBaseCandidate = nil
	s.validatedStateBaseMu.Unlock()
}

func validatedStateBaseProofMatchesLedger(
	proof validatedStateBaseProof,
	h header.LedgerHeader,
	fingerprint [32]byte,
) bool {
	return proof.sequence == h.LedgerIndex && proof.ledgerHash == h.Hash &&
		proof.parentHash == h.ParentHash && proof.stateRoot == h.AccountHash &&
		proof.txRoot == h.TxHash && proof.nodeStoreFingerprint == fingerprint
}

func validatedStateBaseProofCanBeInherited(
	proof validatedStateBaseProof,
	h header.LedgerHeader,
	fingerprint [32]byte,
) bool {
	return proof.nodeStoreFingerprint == fingerprint && proof.ledgerHash == h.ParentHash &&
		proof.sequence != ^uint32(0) && proof.sequence+1 == h.LedgerIndex
}

// fetchDurableNodeData bypasses decoded-node caches. A cached positive read
// can survive managed deletion, so it cannot establish a recovery base.
func (s *Service) fetchDurableNodeData(ctx context.Context, hash nodestore.Hash256) ([]byte, error) {
	if s.nodeStore == nil {
		return nil, errors.New("NodeStore is unavailable")
	}
	if reader, ok := s.nodeStore.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	}); ok {
		return reader.FetchDataUncached(ctx, hash)
	}
	if reader, ok := s.shamapFamily.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}); ok {
		return reader.FetchDurable(ctx, [32]byte(hash))
	}
	return nil, errors.New("NodeStore has no uncached durable read")
}

func (s *Service) verifyDurableSHAMapRoot(ctx context.Context, root [32]byte, name string) error {
	if root == ([32]byte{}) {
		return fmt.Errorf("durable %s root is empty", name)
	}
	data, err := s.fetchDurableNodeData(ctx, nodestore.Hash256(root))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("durable %s root is missing", name)
	}
	node, err := shamap.DeserializeFromPrefix(data)
	if err != nil {
		return fmt.Errorf("decode durable %s root: %w", name, err)
	}
	if _, ok := node.(shamap.InnerNodeReader); !ok || node.Hash() != root {
		return fmt.Errorf("durable %s root does not match its hash", name)
	}
	return nil
}

func (s *Service) durableValidatedHeader(ctx context.Context, h header.LedgerHeader) error {
	data, err := s.fetchDurableNodeData(ctx, nodestore.Hash256(h.Hash))
	if err != nil {
		return fmt.Errorf("read validated state base header: %w", err)
	}
	if data == nil {
		return errors.New("validated state base header is not durable")
	}
	stored, err := header.DeserializeHeader(data, true)
	if err != nil {
		return fmt.Errorf("decode validated state base header: %w", err)
	}
	if stored.Hash != h.Hash || stored.LedgerIndex != h.LedgerIndex ||
		stored.AccountHash != h.AccountHash || stored.TxHash != h.TxHash ||
		header.CalculateHash(*stored) != h.Hash {
		return errors.New("validated state base header does not match validated ledger")
	}
	return nil
}

func (s *Service) durableValidatedTipMatches(ctx context.Context, h header.LedgerHeader) error {
	data, err := s.fetchDurableNodeData(ctx, validatedTipKey)
	if err != nil {
		return fmt.Errorf("read validated state base publication: %w", err)
	}
	if len(data) != len(h.Hash) || !bytes.Equal(data, h.Hash[:]) {
		return errors.New("validated state base is not the durable validated publication")
	}
	return nil
}

func (s *Service) validatedStateBaseCandidateComplete(ctx context.Context, h header.LedgerHeader) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	provider, ok := s.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	if !ok || provider.FullBelowCache() == nil {
		return false
	}
	cache := provider.FullBelowCache()
	generation, release := cache.Begin()
	defer release()
	fingerprint, bound := cache.DurableFingerprint(generation)
	if !bound {
		return false
	}
	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return false
	}
	currentFingerprint, err := durable.DurableFingerprint(ctx)
	if err != nil || currentFingerprint != fingerprint {
		return false
	}
	if !cache.Has(generation, h.AccountHash) {
		return false
	}
	if h.TxHash != ([32]byte{}) && !cache.Has(generation, h.TxHash) {
		return false
	}
	return ctx.Err() == nil
}

func (s *Service) bindValidatedStateBaseCache(fingerprint [32]byte) bool {
	provider, ok := s.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	if !ok || provider.FullBelowCache() == nil {
		return false
	}
	cache := provider.FullBelowCache()
	generation, release := cache.Begin()
	defer release()
	return cache.BindDurableFingerprint(generation, fingerprint)
}

func (s *Service) prepareValidatedStateBaseCache(fingerprint [32]byte) bool {
	provider, ok := s.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	if !ok || provider.FullBelowCache() == nil {
		return false
	}
	cache := provider.FullBelowCache()
	return cache.EnsureDurableFingerprint(fingerprint) != 0
}

// advanceValidatedStateBaseProof extends an existing complete-tree proof over
// one consecutive persisted ledger, or promotes a fully fetched initial-sync
// candidate. It deliberately declines when neither exists: a full tree walk
// belongs to startup/acquisition validation, never to ordinary persistence.
func (s *Service) advanceValidatedStateBaseProof(ctx context.Context, l *ledger.Ledger) error {
	if l == nil || !l.IsValidated() || s.nodeStore == nil || s.shamapFamily == nil {
		return nil
	}
	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return nil
	}
	if _, found := s.currentValidatedStateBaseProof(); !found {
		if _, candidateFound := s.currentValidatedStateBaseCandidate(); !candidateFound {
			return nil
		}
	}
	fingerprint, release, err := durable.AcquireDurableSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("acquire validated state base snapshot: %w", err)
	}
	defer release()
	proof, found := s.currentValidatedStateBaseProof()
	candidate, candidateFound := s.currentValidatedStateBaseCandidate()
	candidateIdentity := candidate
	if !found && !candidateFound {
		return nil
	}
	h := l.Header()
	proofMatches := found && (validatedStateBaseProofCanBeInherited(proof, h, fingerprint) ||
		validatedStateBaseProofMatchesLedger(proof, h, fingerprint))
	if !proofMatches && candidateFound && s.validatedStateBaseCandidateComplete(ctx, h) {
		candidate.nodeStoreFingerprint = fingerprint
		proofMatches = validatedStateBaseProofMatchesLedger(candidate, h, fingerprint)
		if proofMatches {
			proof = candidate
		}
	}
	if !proofMatches {
		s.validatedStateBaseMu.Lock()
		if candidateFound && candidateIdentity.sequence <= h.LedgerIndex && s.validatedStateBaseCandidate != nil &&
			*s.validatedStateBaseCandidate == candidateIdentity {
			s.validatedStateBaseCandidate = nil
		}
		if found && proof.sequence <= h.LedgerIndex && s.validatedStateBaseProof != nil && *s.validatedStateBaseProof == proof {
			s.validatedStateBaseProof = nil
		}
		s.validatedStateBaseMu.Unlock()
		return nil
	}
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
	if err := s.durableValidatedTipMatches(ctx, h); err != nil {
		return err
	}
	current, err := durable.DurableFingerprint(ctx)
	if err != nil {
		return fmt.Errorf("recheck validated state base generation: %w", err)
	}
	if current != fingerprint {
		return errors.New("validated state base generation changed during proof")
	}
	s.rememberValidatedStateBase(h, fingerprint)
	return nil
}

func (s *Service) tryAdvanceValidatedStateBaseProof(ctx context.Context, l *ledger.Ledger) {
	if err := s.advanceValidatedStateBaseProof(ctx, l); err != nil {
		s.logger.Warn("validated state base proof unavailable", "sequence", l.Sequence(), "err", err)
	}
}
