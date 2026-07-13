package service

import (
	"bytes"
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

func (s *Service) loadLatestLedger(ctx context.Context) (*ledger.Ledger, error) {
	if s.nodeStore == nil || s.relationalDB == nil || s.shamapFamily == nil {
		return nil, nil
	}
	info, err := s.relationalDB.Ledger().GetNewestLedgerInfo(ctx)
	if err != nil || info == nil {
		return nil, err
	}
	tip, err := s.nodeStore.Fetch(ctx, validatedTipKey)
	if err != nil {
		return nil, err
	}
	if tip == nil {
		return nil, nil
	}
	if tip.Type != nodestore.NodeLedger || tip.LedgerSeq != uint32(info.Sequence) ||
		len(tip.Data) != 32 || !bytes.Equal(tip.Data, info.Hash[:]) {
		return nil, fmt.Errorf("newest relational ledger %d is not the persisted validated tip", info.Sequence)
	}
	stored, err := s.nodeStore.Fetch(ctx, nodestore.Hash256(info.Hash))
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Type != nodestore.NodeLedger {
		return nil, fmt.Errorf("ledger %d header is missing", info.Sequence)
	}
	h, err := header.DeserializeHeader(stored.Data, true)
	if err != nil {
		return nil, err
	}
	if h.Hash != [32]byte(info.Hash) || h.LedgerIndex != uint32(info.Sequence) ||
		h.AccountHash != [32]byte(info.AccountHash) || h.TxHash != [32]byte(info.TransactionHash) ||
		h.ParentHash != [32]byte(info.ParentHash) || h.Drops != uint64(info.TotalCoins) ||
		!h.CloseTime.Equal(info.CloseTime) || !h.ParentCloseTime.Equal(info.ParentCloseTime) ||
		h.CloseTimeResolution != uint32(info.CloseTimeRes) || h.CloseFlags != uint8(info.CloseFlags) ||
		header.CalculateHash(*h) != h.Hash {
		return nil, fmt.Errorf("ledger %d header does not match persisted metadata", info.Sequence)
	}
	if err := s.verifyStoredSHAMap(ctx, h.AccountHash, shamap.TypeState); err != nil {
		return nil, fmt.Errorf("ledger %d state tree: %w", info.Sequence, err)
	}
	if h.TxHash != ([32]byte{}) {
		if err := s.verifyStoredSHAMap(ctx, h.TxHash, shamap.TypeTransaction); err != nil {
			return nil, fmt.Errorf("ledger %d transaction tree: %w", info.Sequence, err)
		}
	}

	stateMap, err := shamap.NewFromRootHash(shamap.TypeState, h.AccountHash, s.shamapFamily)
	if err != nil {
		return nil, err
	}
	var txMap *shamap.SHAMap
	if h.TxHash == ([32]byte{}) {
		txMap, err = shamap.NewBacked(shamap.TypeTransaction, s.shamapFamily)
	} else {
		txMap, err = shamap.NewFromRootHash(shamap.TypeTransaction, h.TxHash, s.shamapFamily)
	}
	if err != nil {
		return nil, err
	}
	stateMap.SetLedgerSeq(h.LedgerIndex)
	txMap.SetLedgerSeq(h.LedgerIndex)
	if err := stateMap.SetImmutable(); err != nil {
		return nil, err
	}
	if err := txMap.SetImmutable(); err != nil {
		return nil, err
	}
	h.Validated = true
	h.Accepted = true
	return ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{}), nil
}

func (s *Service) verifyStoredSHAMap(ctx context.Context, root [32]byte, mapType shamap.Type) error {
	return s.walkStoredSHAMap(ctx, root, mapType, nil)
}

func (s *Service) walkStoredSHAMap(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	visit func([32]byte, *nodestore.Node) error,
) error {
	if root == ([32]byte{}) {
		return fmt.Errorf("zero root")
	}
	type pendingNode struct {
		hash  [32]byte
		depth int
	}
	stack := []pendingNode{{hash: root}}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		pending := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		stored, err := s.nodeStore.Fetch(ctx, nodestore.Hash256(pending.hash))
		if err != nil {
			return err
		}
		if stored == nil {
			return fmt.Errorf("node %x is missing", pending.hash[:8])
		}
		node, err := shamap.DeserializeFromPrefix(stored.Data)
		if err != nil {
			return err
		}
		if node.Hash() != pending.hash {
			return fmt.Errorf("node %x has invalid content hash", pending.hash[:8])
		}
		if inner, ok := node.(shamap.InnerNodeReader); ok {
			if pending.depth >= 64 {
				return fmt.Errorf("inner node %x exceeds maximum depth", pending.hash[:8])
			}
			for branch := 0; branch < shamap.BranchFactor; branch++ {
				if inner.IsEmptyBranch(branch) {
					continue
				}
				child, err := inner.ChildHash(branch)
				if err != nil {
					return err
				}
				stack = append(stack, pendingNode{hash: child, depth: pending.depth + 1})
			}
		} else if pending.depth == 0 {
			return fmt.Errorf("root node %x is not an inner node", pending.hash[:8])
		} else if mapType == shamap.TypeState && node.Type() != shamap.NodeTypeAccountState {
			return fmt.Errorf("state tree contains %s leaf", node.Type())
		} else if mapType == shamap.TypeTransaction &&
			node.Type() != shamap.NodeTypeTransactionNoMeta &&
			node.Type() != shamap.NodeTypeTransactionWithMeta {
			return fmt.Errorf("transaction tree contains %s leaf", node.Type())
		}
		if visit != nil {
			if err := visit(pending.hash, stored); err != nil {
				return err
			}
		}
	}
	return nil
}
