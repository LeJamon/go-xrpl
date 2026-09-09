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
	stateMap, err := parent.stateMap.SnapshotMutable()
	parent.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot state map: %w", err)
	}

	txMap := shamap.New(shamap.TypeTransaction)

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
	stateMap.SetLedgerSeq(newLedgerSeq)
	txMap.SetLedgerSeq(newLedgerSeq)

	return &Ledger{
		stateMap:       stateMap,
		txMap:          txMap,
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
) (*Ledger, error) {
	hdr.Validated = true
	return newFromHeaderContext(context.Background(), hdr, stateMap, txMap, fees, StateValidated)
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
) (*Ledger, error) {
	state := StateClosed
	if hdr.Validated {
		state = StateValidated
	}
	return newFromHeaderContext(context.Background(), hdr, stateMap, txMap, fees, state)
}

// NewClosedFromHeaderContext reconstructs a closed ledger while forwarding ctx
// to amendment-state reads from lazily backed maps.
func NewClosedFromHeaderContext(
	ctx context.Context,
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
) (*Ledger, error) {
	return newFromHeaderContext(ctx, hdr, stateMap, txMap, fees, StateClosed)
}

func newFromHeaderContext(
	ctx context.Context,
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
	state State,
) (*Ledger, error) {
	if err := validateMaps(stateMap, txMap); err != nil {
		return nil, err
	}
	immutableState, err := stateMap.SnapshotImmutableWithLedgerSeqContext(ctx, hdr.LedgerIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot state map: %w", err)
	}
	immutableTx, err := txMap.SnapshotImmutableWithLedgerSeqContext(ctx, hdr.LedgerIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot transaction map: %w", err)
	}
	rules, err := LoadAmendmentsFromSHAMapContext(ctx, immutableState)
	if err != nil {
		return nil, fmt.Errorf("failed to load amendment rules: %w", err)
	}
	return &Ledger{
		stateMap: immutableState,
		txMap:    immutableTx,
		header:   hdr,
		fees:     fees,
		state:    state,
		writable: false,
		rules:    rules,
	}, nil
}

// NewOpenWithHeader creates an open ledger with the exact header values provided
// (testing/replay scenarios that control all header fields).
func NewOpenWithHeader(
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
) (*Ledger, error) {
	if err := validateMaps(stateMap, txMap); err != nil {
		return nil, err
	}
	ownedState, err := stateMap.SnapshotMutableWithLedgerSeq(hdr.LedgerIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot state map: %w", err)
	}
	ownedTx, err := txMap.SnapshotMutableWithLedgerSeq(hdr.LedgerIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot transaction map: %w", err)
	}
	rules, err := loadAmendmentsFromSHAMap(ownedState)
	if err != nil {
		return nil, fmt.Errorf("failed to load amendment rules: %w", err)
	}
	return &Ledger{
		stateMap: ownedState,
		txMap:    ownedTx,
		header:   hdr,
		fees:     fees,
		state:    StateOpen,
		writable: true,
		rules:    rules,
	}, nil
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
