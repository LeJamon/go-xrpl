package service

import (
	"context"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/shamap"
)

// rememberLocalChildReplayProof records state-only provenance for a child built
// from a generation-bound complete parent. The transaction tree remains local
// until persistence finishes, and generation changes invalidate the record.
func (s *Service) rememberLocalChildReplayProof(ctx context.Context, parent, child *ledger.Ledger) (bool, error) {
	if parent == nil || child == nil || child.IsOpen() ||
		parent.Sequence() == ^uint32(0) || child.Sequence() != parent.Sequence()+1 ||
		child.ParentHash() != parent.Hash() || !s.hasDurableFamily() {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	pin, pinned, err := s.acquireReplaySnapshot(ctx)
	if err != nil {
		return false, err
	}
	if !pinned {
		return false, nil
	}
	defer pin.Release()
	fingerprint := pin.fingerprint

	stateMap, err := child.StateMapSnapshotContext(ctx)
	if err != nil {
		return false, err
	}
	if !stateMap.IsBacked() {
		return false, nil
	}
	stateRoot, err := child.StateMapHash()
	if err != nil {
		return false, err
	}
	h := child.Header()
	if stateRoot == ([32]byte{}) || stateRoot != h.AccountHash {
		return false, nil
	}

	// A parent proof is the only shortcut that justifies inheriting the
	// complete state tree. Do not seed a proof from the child itself.
	if !s.lookupReplayProof(parent.Header(), fingerprint, false) {
		return false, nil
	}
	if s.lookupReplayProof(h, fingerprint, true) {
		return true, nil
	}
	s.rememberReplayProof(h, fingerprint, false)
	return true, nil
}

// transferRetainedReplayProofs records durable completeness for currently owned
// closed ledgers whose trees were retained by the rotation plan. Query-cache
// entries are not owners and cannot be certified here.
func (s *Service) transferRetainedReplayProofs(
	preserved map[retainedStateRoot]struct{},
	fingerprint [32]byte,
) {
	if fingerprint == ([32]byte{}) {
		return
	}
	owners := s.stateRetentionLedgersLocked()
	// stateRetentionLedgersLocked puts the canonical validated owner first and
	// newer owners ahead of older history. Insert in reverse so the bounded LRU
	// retains the validated tip and newest closed/candidate owners.
	for i := len(owners) - 1; i >= 0; i-- {
		l := owners[i]
		if l == nil || l.IsOpen() {
			continue
		}
		h := l.Header()
		if h.AccountHash == ([32]byte{}) {
			continue
		}
		if _, ok := preserved[retainedStateRoot{hash: h.AccountHash, kind: shamap.TypeState}]; !ok {
			continue
		}
		if h.TxHash != ([32]byte{}) {
			if _, ok := preserved[retainedStateRoot{hash: h.TxHash, kind: shamap.TypeTransaction}]; !ok {
				continue
			}
		}
		s.rememberReplayProof(h, fingerprint, true)
	}
}
