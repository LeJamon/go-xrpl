package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

var errStoredLedgerUnavailable = errors.New("stored ledger is incomplete or invalid")

func isUnavailableSHAMapNode(err error) bool {
	return errors.Is(err, shamap.ErrNodeNotInStore) || errors.Is(err, shamap.ErrInvalidNodeData)
}

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
	loaded, err := s.loadStoredLedgerByHash(ctx, [32]byte(info.Hash))
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		return nil, fmt.Errorf("ledger %d header is missing", info.Sequence)
	}
	h := loaded.Header()
	if !storedHeaderMatchesInfo(h, info) {
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
	if err := loaded.SetValidated(); err != nil {
		return nil, fmt.Errorf("mark newest ledger %d validated: %w", info.Sequence, err)
	}
	return loaded, nil
}

func (s *Service) loadStoredLedgerByHash(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	if s.nodeStore == nil || s.shamapFamily == nil {
		return nil, nil
	}
	stored, err := s.nodeStore.Fetch(ctx, nodestore.Hash256(hash))
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, nil
	}
	if stored.Type != nodestore.NodeLedger {
		return nil, fmt.Errorf("%w: stored object is %s, not a ledger header", errStoredLedgerUnavailable, stored.Type)
	}
	h, err := header.DeserializeHeader(stored.Data, true)
	if err != nil {
		return nil, fmt.Errorf("%w: deserialize ledger header: %v", errStoredLedgerUnavailable, err)
	}
	if h.Hash != hash || header.CalculateHash(*h) != hash {
		return nil, fmt.Errorf("%w: stored ledger header does not match requested hash", errStoredLedgerUnavailable)
	}
	if stored.LedgerSeq != 0 && stored.LedgerSeq != h.LedgerIndex {
		return nil, fmt.Errorf("%w: stored ledger sequence %d does not match header %d", errStoredLedgerUnavailable, stored.LedgerSeq, h.LedgerIndex)
	}

	stateMap, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, h.AccountHash, s.shamapFamily)
	if err != nil {
		if isUnavailableSHAMapNode(err) {
			return nil, fmt.Errorf("%w: state root: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		return nil, err
	}
	if stateRoot != h.AccountHash {
		return nil, fmt.Errorf("%w: stored state root does not match ledger header", errStoredLedgerUnavailable)
	}
	var txMap *shamap.SHAMap
	if h.TxHash == ([32]byte{}) {
		txMap, err = shamap.NewBacked(shamap.TypeTransaction, s.shamapFamily)
	} else {
		txMap, err = shamap.NewFromRootHashContext(ctx, shamap.TypeTransaction, h.TxHash, s.shamapFamily)
	}
	if err != nil {
		if isUnavailableSHAMapNode(err) {
			return nil, fmt.Errorf("%w: transaction root: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	txRoot, err := txMap.Hash()
	if err != nil {
		return nil, err
	}
	if txRoot != h.TxHash {
		return nil, fmt.Errorf("%w: stored transaction root does not match ledger header", errStoredLedgerUnavailable)
	}
	stateMap.SetLedgerSeq(h.LedgerIndex)
	txMap.SetLedgerSeq(h.LedgerIndex)
	if err := stateMap.SetImmutable(); err != nil {
		return nil, err
	}
	if err := txMap.SetImmutable(); err != nil {
		return nil, err
	}
	rules, err := ledger.LoadAmendmentsFromSHAMapContext(ctx, stateMap)
	if err != nil {
		if isUnavailableSHAMapNode(err) {
			return nil, fmt.Errorf("%w: amendment state: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	fees, err := storedLedgerFees(ctx, stateMap, rules.XRPFeesEnabled(), s.configuredFees)
	if err != nil {
		if isUnavailableSHAMapNode(err) || errors.Is(err, state.ErrInvalidFeeSettings) || errors.Is(err, errStoredLedgerUnavailable) {
			return nil, fmt.Errorf("%w: fee settings: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	h.Validated = false
	h.Accepted = true
	loaded, err := ledger.NewClosedFromHeaderContext(ctx, *h, stateMap, txMap, fees)
	if err != nil && isUnavailableSHAMapNode(err) {
		return nil, fmt.Errorf("%w: ledger state: %v", errStoredLedgerUnavailable, err)
	}
	return loaded, err
}

func storedHeaderMatchesInfo(h header.LedgerHeader, info *relationaldb.LedgerInfo) bool {
	return info != nil &&
		h.Hash == [32]byte(info.Hash) && h.LedgerIndex == uint32(info.Sequence) &&
		h.AccountHash == [32]byte(info.AccountHash) && h.TxHash == [32]byte(info.TransactionHash) &&
		h.ParentHash == [32]byte(info.ParentHash) && h.Drops == uint64(info.TotalCoins) &&
		h.CloseTime.Equal(info.CloseTime) && h.ParentCloseTime.Equal(info.ParentCloseTime) &&
		h.CloseTimeResolution == uint32(info.CloseTimeRes) && h.CloseFlags == uint8(info.CloseFlags) &&
		header.CalculateHash(h) == h.Hash
}

func storedLedgerFees(ctx context.Context, stateMap *shamap.SHAMap, xrpFeesEnabled bool, fees drops.Fees) (drops.Fees, error) {
	item, found, err := stateMap.GetContext(ctx, keylet.Fees().Key)
	if err != nil {
		return drops.Fees{}, fmt.Errorf("read stored fee settings: %w", err)
	}
	if !found || item == nil {
		return fees, nil
	}
	settings, err := state.ParseFeeSettings(item.Data())
	if err != nil {
		return drops.Fees{}, fmt.Errorf("parse stored fee settings: %w", err)
	}
	if settings.IsUsingModernFees() && !xrpFeesEnabled {
		return drops.Fees{}, fmt.Errorf("%w: XRPFees fields are present before the amendment is enabled", errStoredLedgerUnavailable)
	}
	return mergeFeeSettings(fees, settings), nil
}

func mergeFeeSettings(fees drops.Fees, settings *state.FeeSettings) drops.Fees {
	if settings.HasBaseFeeDrops {
		fees.Base = drops.XRPAmount(settings.BaseFeeDrops)
	}
	if settings.HasReserveBaseDrops {
		fees.Reserve = drops.XRPAmount(settings.ReserveBaseDrops)
	}
	if settings.HasReserveIncrementDrops {
		fees.Increment = drops.XRPAmount(settings.ReserveIncrementDrops)
	}
	if settings.HasBaseFee {
		fees.Base = drops.XRPAmount(settings.BaseFee)
	}
	if settings.HasReserveBase {
		fees.Reserve = drops.XRPAmount(settings.ReserveBase)
	}
	if settings.HasReserveIncrement {
		fees.Increment = drops.XRPAmount(settings.ReserveIncrement)
	}
	return fees
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
