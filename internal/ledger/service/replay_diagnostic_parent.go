package service

import (
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

// prepareReplayDiagnosticParent opens fresh, durable trees, not aliases of the
// failed execution's cached state. Keep the generation pinned through the retry.
// An exact generation-bound completeness certificate avoids a full state copy;
// without one the ordinary bounded-memory verifier proves both trees first.
// The returned handled flag is false only for stores without this capability.
func (s *Service) prepareReplayDiagnosticParent(ctx context.Context, h header.LedgerHeader, fees drops.Fees, reuseProof bool) (_ *ledger.Ledger, release func(), handled bool, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, true, err
	}
	if !s.hasDurableFamily() {
		return nil, nil, false, nil
	}
	if h.Hash == ([32]byte{}) || header.CalculateHash(h) != h.Hash {
		return nil, nil, true, errReplayParentCorrupt
	}
	pin, pinned, err := s.acquireReplaySnapshot(ctx)
	if err != nil {
		return nil, nil, true, err
	}
	if !pinned {
		return nil, nil, false, nil
	}
	owned := true
	defer func() {
		if owned {
			pin.Release()
		}
	}()
	family := durableAdmissionFamily{Family: s.shamapFamily, durable: s.shamapFamily.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	})}
	load := func(typ shamap.Type, root [32]byte) (*shamap.SHAMap, error) {
		if root == ([32]byte{}) {
			return shamap.NewBacked(typ, family)
		}
		return shamap.NewFromRootHashContext(ctx, typ, root, family)
	}
	stateMap, err := load(shamap.TypeState, h.AccountHash)
	if err != nil {
		return nil, nil, true, fmt.Errorf("diagnostic parent state: %w", err)
	}
	txMap, err := load(shamap.TypeTransaction, h.TxHash)
	if err != nil {
		return nil, nil, true, fmt.Errorf("diagnostic parent transactions: %w", err)
	}
	stateProof := reuseProof && s.lookupReplayProof(h, pin.fingerprint, false)
	txProof := h.TxHash == ([32]byte{}) || (reuseProof && s.lookupReplayProof(h, pin.fingerprint, true))
	s.logger.Info("Replay diagnostic parent verification", "sequence", h.LedgerIndex,
		"state_proof_reused", stateProof, "tx_proof_reused", txProof)
	if !stateProof {
		if err := s.verifyDetachedSnapshot(ctx, "replay diagnostic state", stateMap, [32]byte{}); err != nil {
			return nil, nil, true, err
		}
	}
	if !txProof {
		if err := s.verifyDetachedSnapshot(ctx, "replay diagnostic transactions", txMap, [32]byte{}); err != nil {
			return nil, nil, true, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, true, err
	}
	if err := stateMap.SetImmutable(); err != nil {
		return nil, nil, true, err
	}
	if err := txMap.SetImmutable(); err != nil {
		return nil, nil, true, err
	}
	parent, err := ledger.NewFromHeader(h, stateMap, txMap, fees, s.config.Execution)
	if err != nil {
		return nil, nil, true, err
	}
	owned = false
	return parent, pin.Release, true, nil
}
