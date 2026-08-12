package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

func (s *Service) CleanerLedger(ctx context.Context, seq uint32) (*ledger.Ledger, error) {
	hash, ok, err := s.CanonicalLedgerHash(ctx, seq)
	if err != nil || !ok {
		return nil, err
	}
	canonical, err := s.cleanerLedgerByHash(ctx, hash)
	if errors.Is(err, ErrLedgerNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if canonical.Sequence() != seq {
		return nil, fmt.Errorf("ledger_cleaner: canonical hash for ledger %d resolved to ledger %d", seq, canonical.Sequence())
	}
	return canonical, nil
}

func (s *Service) CleanerReacquireTarget(ctx context.Context, seq uint32) ([32]byte, uint32, bool, error) {
	canonical, ok, err := s.CanonicalLedgerHash(ctx, seq)
	if err != nil || ok {
		return canonical, seq, ok, err
	}

	s.mu.RLock()
	tip := s.validatedLedger
	s.mu.RUnlock()
	if tip == nil || seq == 0 || seq > tip.Sequence() {
		return [32]byte{}, 0, false, nil
	}
	if seq == tip.Sequence() {
		return tip.Hash(), seq, true, nil
	}

	hash, ok, err := tip.HashOfSeqContext(ctx, seq)
	if canonicalProofUnavailable(err) {
		return tip.Hash(), tip.Sequence(), true, nil
	}
	if err != nil || ok {
		return hash, seq, ok, err
	}
	if seq%256 == 0 {
		return [32]byte{}, 0, false, nil
	}
	anchor64 := uint64(seq) + uint64(256-seq%256)
	if anchor64 > uint64(tip.Sequence()) {
		return [32]byte{}, 0, false, nil
	}
	anchor := uint32(anchor64)
	hash, ok, err = tip.HashOfSeqContext(ctx, anchor)
	if canonicalProofUnavailable(err) {
		return tip.Hash(), tip.Sequence(), true, nil
	}
	return hash, anchor, ok, err
}

func (s *Service) CanonicalLedgerHash(ctx context.Context, seq uint32) ([32]byte, bool, error) {
	s.mu.RLock()
	tip := s.validatedLedger
	s.mu.RUnlock()
	if tip == nil || seq > tip.Sequence() {
		return [32]byte{}, false, nil
	}
	if seq == tip.Sequence() {
		return tip.Hash(), true, nil
	}
	hash, ok, err := tip.HashOfSeqContext(ctx, seq)
	if canonicalProofUnavailable(err) {
		return [32]byte{}, false, nil
	}
	if err != nil || ok {
		return hash, ok, err
	}
	if seq%256 == 0 {
		return [32]byte{}, false, nil
	}
	anchor64 := uint64(seq) + uint64(256-seq%256)
	if anchor64 > uint64(tip.Sequence()) {
		return [32]byte{}, false, nil
	}
	anchorHash, ok, err := tip.HashOfSeqContext(ctx, uint32(anchor64))
	if canonicalProofUnavailable(err) {
		return [32]byte{}, false, nil
	}
	if err != nil || !ok {
		return [32]byte{}, false, err
	}
	anchor, err := s.cleanerLedgerByHash(ctx, anchorHash)
	if errors.Is(err, ErrLedgerNotFound) {
		return [32]byte{}, false, nil
	}
	if err != nil {
		return [32]byte{}, false, err
	}
	hash, ok, err = anchor.HashOfSeqContext(ctx, seq)
	if canonicalProofUnavailable(err) {
		return [32]byte{}, false, nil
	}
	return hash, ok, err
}

func (s *Service) cleanerLedgerByHash(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	s.historyComponent.mu.RLock()
	cached := s.persistedLedgers[hash]
	s.historyComponent.mu.RUnlock()
	if cached != nil && cached.Hash() == hash {
		return cached, nil
	}

	s.mu.RLock()
	validated := s.validatedLedger
	closed := s.closedLedger
	s.mu.RUnlock()
	for _, candidate := range []*ledger.Ledger{validated, closed} {
		if candidate != nil && candidate.Hash() == hash {
			return candidate, nil
		}
	}
	if s.nodeStore == nil || s.shamapFamily == nil || s.relationalDB == nil || s.relationalDB.Ledger() == nil {
		return nil, ErrLedgerNotFound
	}
	loaded, err := s.loadPersistedLedgerByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	s.historyComponent.mu.Lock()
	s.cachePersistedLedgerLocked(loaded)
	s.historyComponent.mu.Unlock()
	return loaded, nil
}

func (s *Service) RepairCleanerLedgerIndex(
	ctx context.Context,
	seq uint32,
	hash, parentHash [32]byte,
) (bool, error) {
	repairTransactions := false

	if s.relationalDB != nil && s.relationalDB.Ledger() != nil {
		info, err := s.relationalDB.Ledger().GetLedgerInfoBySeq(
			ctx, relationaldb.LedgerIndex(seq),
		)
		switch {
		case errors.Is(err, relationaldb.ErrLedgerNotFound):
			repairTransactions = true
		case err != nil:
			return false, err
		case info == nil ||
			[32]byte(info.Hash) != hash ||
			[32]byte(info.ParentHash) != parentHash:
			repairTransactions = true
		}
	}

	s.mu.Lock()
	s.historyComponent.mu.Lock()
	indexed := s.ledgerHistory[seq]
	if indexed == nil || indexed.Hash() != hash || indexed.ParentHash() != parentHash {
		repairTransactions = true
		canonical := s.persistedLedgers[hash]
		if canonical == nil {
			switch {
			case s.validatedLedger != nil && s.validatedLedger.Hash() == hash:
				canonical = s.validatedLedger
			case s.closedLedger != nil && s.closedLedger.Hash() == hash:
				canonical = s.closedLedger
			}
		}
		if canonical != nil && canonical.Sequence() == seq && canonical.ParentHash() == parentHash {
			s.putHistoryLocked(canonical)
			indexed = canonical
		}
	}
	if indexed == nil || indexed.Hash() != hash || indexed.ParentHash() != parentHash {
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		return false, fmt.Errorf("ledger_cleaner: canonical ledger %d unavailable for index repair", seq)
	}
	if indexedSeq, ok := s.ledgerByHash[hash]; !ok || indexedSeq != seq {
		s.ledgerByHash[hash] = seq
		repairTransactions = true
	}
	for indexedHash, indexedSeq := range s.ledgerByHash {
		if indexedSeq == seq && indexedHash != hash {
			delete(s.ledgerByHash, indexedHash)
			repairTransactions = true
		}
	}
	s.historyComponent.mu.Unlock()
	s.mu.Unlock()

	return repairTransactions, nil
}
