package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

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
		return nil, fmt.Errorf("%w: stored object has type %d, not a ledger header", errStoredLedgerUnavailable, stored.Type)
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
	startedAt := time.Now()
	ticker := time.NewTicker(storedSHAMapVerificationLogInterval)
	defer ticker.Stop()
	return s.verifyStoredSHAMapWithTicks(ctx, root, mapType, startedAt, time.Now, ticker.C)
}

func (s *Service) verifyStoredSHAMapWithTicks(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	startedAt time.Time,
	now func() time.Time,
	ticks <-chan time.Time,
) (err error) {
	progress := newStoredSHAMapVerificationProgress(s.logger, root, mapType, startedAt)
	defer func() {
		progress.finish(now(), err)
	}()

	if root == ([32]byte{}) {
		return fmt.Errorf("zero root")
	}

	rootNode, _, err := s.loadStoredSHAMapNode(ctx, storedSHAMapNode{hash: root}, mapType)
	if err != nil {
		return err
	}
	progress.nodesChecked.Add(1)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	if !ok {
		return fmt.Errorf("root node %x is not an inner node", root[:8])
	}

	branches := make([][32]byte, 0, shamap.BranchFactor)
	for branch := range shamap.BranchFactor {
		if inner.IsEmptyBranch(branch) {
			continue
		}
		child, childErr := inner.ChildHash(branch)
		if childErr != nil {
			return childErr
		}
		branches = append(branches, child)
	}
	progress.branchesTotal = uint32(len(branches))
	progress.start()

	walkCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	for _, child := range branches {
		wg.Add(1)
		go func(child [32]byte) {
			defer wg.Done()
			var unreportedNodes uint64
			walkErr := s.walkStoredSHAMapNodes(
				walkCtx,
				[]storedSHAMapNode{{hash: child, depth: 1}},
				mapType,
				func([32]byte, *nodestore.Node) error {
					unreportedNodes++
					if unreportedNodes == storedSHAMapNodeCountBatch {
						progress.nodesChecked.Add(unreportedNodes)
						unreportedNodes = 0
					}
					return nil
				},
			)
			if unreportedNodes > 0 {
				progress.nodesChecked.Add(unreportedNodes)
			}
			if walkErr != nil {
				cancel(walkErr)
				return
			}
			progress.branchesComplete.Add(1)
		}(child)
	}
	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	for {
		select {
		case tick, ok := <-ticks:
			if !ok {
				ticks = nil
				continue
			}
			progress.report(tick)
		case <-workersDone:
			progress.report(now())
			return context.Cause(walkCtx)
		}
	}
}

type storedSHAMapNode struct {
	hash  [32]byte
	depth int
}

type storedSHAMapFetch func(context.Context, nodestore.Hash256) (*nodestore.Node, error)

func (s *Service) walkStoredSHAMap(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	visit func([32]byte, *nodestore.Node) error,
) error {
	return s.walkStoredSHAMapWithFetch(ctx, root, mapType, s.nodeStore.Fetch, visit)
}

func (s *Service) walkStoredSHAMapWithFetch(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
	visit func([32]byte, *nodestore.Node) error,
) error {
	if root == ([32]byte{}) {
		return fmt.Errorf("zero root")
	}
	return s.walkStoredSHAMapNodesWithFetch(
		ctx,
		[]storedSHAMapNode{{hash: root}},
		mapType,
		fetch,
		visit,
	)
}

func (s *Service) walkStoredSHAMapNodes(
	ctx context.Context,
	stack []storedSHAMapNode,
	mapType shamap.Type,
	visit func([32]byte, *nodestore.Node) error,
) error {
	return s.walkStoredSHAMapNodesWithFetch(ctx, stack, mapType, s.nodeStore.Fetch, visit)
}

func (s *Service) walkStoredSHAMapNodesWithFetch(
	ctx context.Context,
	stack []storedSHAMapNode,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
	visit func([32]byte, *nodestore.Node) error,
) error {
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		pending := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node, stored, err := s.loadStoredSHAMapNodeWithFetch(ctx, pending, mapType, fetch)
		if err != nil {
			return err
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
				stack = append(stack, storedSHAMapNode{hash: child, depth: pending.depth + 1})
			}
		}
		if visit != nil {
			if err := visit(pending.hash, stored); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) loadStoredSHAMapNode(
	ctx context.Context,
	pending storedSHAMapNode,
	mapType shamap.Type,
) (shamap.NodeReader, *nodestore.Node, error) {
	return s.loadStoredSHAMapNodeWithFetch(ctx, pending, mapType, s.nodeStore.Fetch)
}

func (s *Service) loadStoredSHAMapNodeWithFetch(
	ctx context.Context,
	pending storedSHAMapNode,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
) (shamap.NodeReader, *nodestore.Node, error) {
	stored, err := fetch(ctx, nodestore.Hash256(pending.hash))
	if err != nil {
		return nil, nil, err
	}
	if stored == nil {
		return nil, nil, fmt.Errorf("node %x is missing", pending.hash[:8])
	}
	node, err := shamap.DeserializeFromPrefix(stored.Data)
	if err != nil {
		return nil, nil, err
	}
	if node.Hash() != pending.hash {
		return nil, nil, fmt.Errorf("node %x has invalid content hash", pending.hash[:8])
	}
	if _, inner := node.(shamap.InnerNodeReader); inner {
		return node, stored, nil
	}
	if pending.depth == 0 {
		return nil, nil, fmt.Errorf("root node %x is not an inner node", pending.hash[:8])
	}
	if mapType == shamap.TypeState && node.Type() != shamap.NodeTypeAccountState {
		return nil, nil, fmt.Errorf("state tree contains %s leaf", node.Type())
	}
	if mapType == shamap.TypeTransaction &&
		node.Type() != shamap.NodeTypeTransactionNoMeta &&
		node.Type() != shamap.NodeTypeTransactionWithMeta {
		return nil, nil, fmt.Errorf("transaction tree contains %s leaf", node.Type())
	}
	return node, stored, nil
}
