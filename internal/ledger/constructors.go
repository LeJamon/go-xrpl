package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

func NewOpen(parent *Ledger, closeTime time.Time) (*Ledger, error) {
	return newOpen(parent, closeTime, false, nil)
}

// NewOpenWithRules creates an open successor using rules selected by the
// caller. A nil rules value preserves NewOpen's inherited-rules behaviour.
// The rules pointer is captured in the immutable open ledger snapshot and is
// used for every transaction applied to that snapshot.
func NewOpenWithRules(parent *Ledger, closeTime time.Time, rules *amendment.Rules) (*Ledger, error) {
	return newOpen(parent, closeTime, false, rules)
}

// NewOpenForBuild creates the mutable successor used to build a closed ledger.
// Its provisional close time is visible to transactions until Close installs
// the accepted close time.
func NewOpenForBuild(parent *Ledger, closeTime time.Time) (*Ledger, error) {
	return newOpen(parent, closeTime, true, nil)
}

// ApplicationViewCloseTime returns the provisional successor time visible to
// transactions before the accepted close time is installed.
func ApplicationViewCloseTime(parentCloseTime, proposedCloseTime time.Time, resolution uint32) time.Time {
	if resolution == 0 {
		return proposedCloseTime
	}
	if parent := protocol.ToRippleTime(parentCloseTime); parent != 0 {
		return protocol.FromRippleTime(parent + resolution)
	}

	seconds := protocol.ToRippleTime(proposedCloseTime)
	seconds += resolution / 2
	seconds -= seconds % resolution
	return protocol.FromRippleTime(seconds)
}

func newOpen(parent *Ledger, closeTime time.Time, building bool, rulesOverride *amendment.Rules) (*Ledger, error) {
	if parent == nil {
		return nil, errors.New("parent ledger cannot be nil")
	}

	parent.mu.RLock()
	if parent.state == StateOpen || parent.writable {
		parent.mu.RUnlock()
		return nil, errors.New("parent ledger must be finalized")
	}
	if parent.header.LedgerIndex == ^uint32(0) {
		seq := parent.header.LedgerIndex
		parent.mu.RUnlock()
		return nil, fmt.Errorf("parent ledger sequence %d has no successor", seq)
	}
	if parent.rules == nil && rulesOverride == nil {
		parent.mu.RUnlock()
		return nil, errors.New("parent ledger has no amendment rules")
	}
	parentHeader := parent.header
	parentFees := parent.fees
	parentRules := parent.rules
	if rulesOverride != nil {
		parentRules = rulesOverride
	}
	execution, err := parent.execution.successor(parentHeader.LedgerIndex + 1)
	parent.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot state map: %w", err)
	}

	// Recompute close-time resolution per close from the parent's previousAgree
	// (encoded in its CloseFlags) — matches rippled and avoids plumbing
	// previousAgree through every NewOpen caller.
	newLedgerSeq := parentHeader.LedgerIndex + 1
	newResolution := consensus.GetNextLedgerTimeResolution(
		uint32(parentHeader.CloseTimeResolution),
		parentHeader.GetCloseAgree(),
		newLedgerSeq,
	)
	if building {
		closeTime = ApplicationViewCloseTime(parentHeader.CloseTime, closeTime, newResolution)
	}

	newHeader := header.LedgerHeader{
		LedgerIndex:         newLedgerSeq,
		Hash:                incrementHash(parentHeader.Hash),
		ParentHash:          parentHeader.Hash,
		ParentCloseTime:     parentHeader.CloseTime,
		CloseTime:           closeTime,
		CloseTimeResolution: uint8(newResolution),
		Drops:               parentHeader.Drops,
	}
	return &Ledger{
		execution:      execution,
		header:         newHeader,
		fees:           parentFees,
		state:          StateOpen,
		writable:       true,
		dropsDestroyed: 0,
		rules:          parentRules,
	}, nil
}

func incrementHash(hash [32]byte) [32]byte {
	for i := len(hash) - 1; i >= 0; i-- {
		hash[i]++
		if hash[i] != 0 {
			break
		}
	}
	return hash
}

func FromGenesis(
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
	store *ExecutionStore,
) (*Ledger, error) {
	hdr.Validated = true
	return newFromHeaderContext(context.Background(), hdr, stateMap, txMap, fees, StateValidated, store)
}

// NewFromHeader creates a closed ledger from a deserialized header and existing
// state/tx maps. The header's Validated flag determines whether it is already
// validated; peer wire headers omit that local state and remain closed until
// quorum promotion.
func NewFromHeader(
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
	store *ExecutionStore,
) (*Ledger, error) {
	state := StateClosed
	if hdr.Validated {
		state = StateValidated
	}
	return newFromHeaderContext(context.Background(), hdr, stateMap, txMap, fees, state, store)
}

// NewClosedFromHeaderContext reconstructs a closed ledger while forwarding ctx
// to amendment-state reads from lazily backed maps.
func NewClosedFromHeaderContext(
	ctx context.Context,
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
	store *ExecutionStore,
) (*Ledger, error) {
	return newFromHeaderContext(ctx, hdr, stateMap, txMap, fees, StateClosed, store)
}

func newFromHeaderContext(
	ctx context.Context,
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
	state State,
	store *ExecutionStore,
) (*Ledger, error) {
	if state == StateOpen {
		return nil, errors.New("open ledgers must use NewOpenWithHeader")
	}
	view, rules, err := importExecution(ctx, hdr, stateMap, txMap, store, false)
	if err != nil {
		return nil, err
	}
	return &Ledger{execution: view, header: hdr, fees: fees, state: state, rules: rules}, nil
}

// NewOpenWithHeader creates an open ledger with the exact header values provided
// (testing/replay scenarios that control all header fields).
func NewOpenWithHeader(
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
	store *ExecutionStore,
) (*Ledger, error) {
	view, rules, err := importExecution(context.Background(), hdr, stateMap, txMap, store, true)
	if err != nil {
		return nil, err
	}
	return &Ledger{execution: view, header: hdr, fees: fees, state: StateOpen, writable: true, rules: rules}, nil
}

// NewClosedFromRootsContext creates a closed ledger whose trees are already
// represented by roots in store's Family. The roots remain unauthenticated
// until EstablishBoundedClosure is called after the caller's durability or
// verification barrier.
func NewClosedFromRootsContext(
	ctx context.Context,
	hdr header.LedgerHeader,
	fees drops.Fees,
	store *ExecutionStore,
) (*Ledger, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return nil, errors.New("bounded execution store cannot be nil")
	}
	if header.CalculateHash(hdr) != hdr.Hash {
		return nil, errors.New("bounded execution header is invalid")
	}
	view := rootedExecution(hdr, store)
	rules, err := loadAmendmentsFromTree(ctx, view.state)
	if err != nil {
		return nil, fmt.Errorf("failed to load amendment rules: %w", err)
	}
	state := StateClosed
	if hdr.Validated {
		state = StateValidated
	}
	return &Ledger{execution: view, header: hdr, fees: fees, state: state, rules: rules}, nil
}

func rootedExecution(hdr header.LedgerHeader, store *ExecutionStore) *executionView {
	return &executionView{
		state: &executionTree{
			store: store, kind: shamap.TypeState, base: hdr, root: hdr.AccountHash, sequence: hdr.LedgerIndex,
		},
		transactions: &executionTree{
			store: store, kind: shamap.TypeTransaction, base: hdr, root: hdr.TxHash, sequence: hdr.LedgerIndex,
		},
	}
}

func emptyExecution(hdr header.LedgerHeader, store *ExecutionStore) *executionView {
	view := rootedExecution(header.LedgerHeader{LedgerIndex: hdr.LedgerIndex}, store)
	view.state.closure = executionClosureVerified
	view.transactions.closure = executionClosureVerified
	view.transactions.countKnown = true
	return view
}

// importExecution copies the dirty portion of each input map into the shared
// execution Family, then drops the SHAMap representation from the Ledger. The
// input maps are acquisition/import boundaries; all subsequent reads and
// writes use root descriptors and bounded overlays.
func importExecution(
	ctx context.Context,
	hdr header.LedgerHeader,
	stateMap, txMap *shamap.SHAMap,
	store *ExecutionStore,
	writable bool,
) (*executionView, *amendment.Rules, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateMaps(stateMap, txMap); err != nil {
		return nil, nil, err
	}
	if store == nil {
		return nil, nil, errors.New("bounded execution store cannot be nil")
	}
	stateRoot, err := importMap(ctx, stateMap, shamap.TypeState, hdr.LedgerIndex, store)
	if err != nil {
		return nil, nil, fmt.Errorf("persist imported state tree: %w", err)
	}
	txRoot, err := importMap(ctx, txMap, shamap.TypeTransaction, hdr.LedgerIndex, store)
	if err != nil {
		return nil, nil, fmt.Errorf("persist imported transaction tree: %w", err)
	}
	if !writable {
		if header.CalculateHash(hdr) != hdr.Hash {
			return nil, nil, errors.New("bounded execution header is invalid")
		}
		if stateRoot != hdr.AccountHash || txRoot != hdr.TxHash {
			return nil, nil, errors.New("bounded execution roots do not match ledger header")
		}
	}
	view := &executionView{
		state: &executionTree{
			store: store, kind: shamap.TypeState, base: hdr, root: stateRoot, sequence: hdr.LedgerIndex,
		},
		transactions: &executionTree{
			store: store, kind: shamap.TypeTransaction, base: hdr, root: txRoot, sequence: hdr.LedgerIndex,
		},
	}
	if !writable {
		if err := verifyExecutionRoot(ctx, view.state); err != nil {
			return nil, nil, fmt.Errorf("verify imported state closure: %w", err)
		}
		txCount, err := countExecutionRoot(ctx, view.transactions)
		if err != nil {
			return nil, nil, fmt.Errorf("verify imported transaction closure: %w", err)
		}
		if txCount > uint64(^uint32(0)) {
			return nil, nil, errors.New("transaction leaf count exceeds uint32")
		}
		view.transactions.count, view.transactions.countKnown = uint32(txCount), true
		view.state.closure = executionClosurePending
		view.transactions.closure = executionClosurePending
		rules, err := loadAmendmentsFromTree(ctx, view.state)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load amendment rules: %w", err)
		}
		return view, rules, nil
	}
	state, err := view.state.newMutable(hdr.LedgerIndex)
	if err != nil {
		return nil, nil, fmt.Errorf("open imported state tree: %w", err)
	}
	txs, err := view.transactions.newMutable(hdr.LedgerIndex)
	if err != nil {
		state.close()
		return nil, nil, fmt.Errorf("open imported transaction tree: %w", err)
	}
	view = &executionView{state: state, transactions: txs}
	rules, err := loadAmendmentsFromTree(ctx, view.state)
	if err != nil {
		view.close()
		return nil, nil, fmt.Errorf("failed to load amendment rules: %w", err)
	}
	return view, rules, nil
}

func importMap(ctx context.Context, source *shamap.SHAMap, kind shamap.Type, seq uint32, store *ExecutionStore) ([32]byte, error) {
	if source.Type() != kind {
		return [32]byte{}, fmt.Errorf("map has type %s, want %s", source.Type(), kind)
	}
	copy, err := source.MutableFork()
	if err != nil {
		return [32]byte{}, err
	}
	copy.SetLedgerSeq(seq)
	root, err := copy.Hash()
	if err != nil {
		return [32]byte{}, err
	}
	persist := copy.StoreDirtyBatched
	if !source.IsBacked() {
		persist = copy.StoreAllBatched
	}
	if err := persist(ctx, store.config.Workspace, func(entries []shamap.FlushEntry) error {
		return store.config.Family.StoreBatch(ctx, entries)
	}); err != nil {
		return [32]byte{}, err
	}
	return root, nil
}

func validateMaps(stateMap, txMap *shamap.SHAMap) error {
	if stateMap == nil {
		return errors.New("state map cannot be nil")
	}
	if txMap == nil {
		return errors.New("transaction map cannot be nil")
	}
	if stateMap.Type() != shamap.TypeState {
		return fmt.Errorf("state map has type %s, want %s", stateMap.Type(), shamap.TypeState)
	}
	if txMap.Type() != shamap.TypeTransaction {
		return fmt.Errorf("transaction map has type %s, want %s", txMap.Type(), shamap.TypeTransaction)
	}
	return nil
}
