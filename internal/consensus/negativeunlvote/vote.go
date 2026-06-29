// Package negativeunlvote decides whether to inject a UNLModify pseudo-tx
// into the consensus tx set at a flag-ledger boundary, scoring each
// validator's participation over the last FlagLedgerInterval ledgers and
// picking candidates deterministically. Mirrors rippled NegativeUNLVote.
package negativeunlvote

import (
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/common"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/protocol"
)

// ErrLocalCountExceedsWindow signals the local node's validation count
// exceeded FlagLedgerInterval — an impossible state pointing to an upstream
// bug. Callers treat it as a no-vote (nil blobs) and surface it for visibility.
var ErrLocalCountExceedsWindow = errors.New("negativeunlvote: local validation count exceeds flag-ledger window")

const (
	// below this score a validator is unreliable → ToDisable candidate
	LowWaterMark uint32 = protocol.FlagLedgerInterval * 50 / 100

	// above this score a disabled validator → ToReEnable candidate
	HighWaterMark uint32 = protocol.FlagLedgerInterval * 80 / 100

	// minimum local validations to trust our own view; below it, abstain
	MinLocalValsToVote uint32 = protocol.FlagLedgerInterval * 90 / 100

	// ledgers a freshly-added validator is exempt from ToDisable voting
	NewValidatorDisableSkip uint32 = protocol.FlagLedgerInterval * 2

	// max fraction of the UNL that may be on the NegativeUNL at once
	MaxListedFraction float64 = 0.25
)

// Modify identifies the direction of a UNLModify pseudo-tx.
type Modify uint8

const (
	ToDisable  Modify = 1 // UNLModify with sfUNLModifyDisabling=1
	ToReEnable Modify = 0 // UNLModify with sfUNLModifyDisabling=0
)

// State captures the parent ledger's NegativeUNL entry: the disabled set
// plus any pending change not yet in effect.
//
// Invariant: ToDisablePending and ToReEnablePending must not be the same
// key (the UNLModify tx layer enforces it). The producer relies on this —
// aliasing them would silently drop the validator from effectiveNegUNL.
type State struct {
	// master pubkeys currently on the negUNL (excluded from quorum)
	DisabledKeys [][33]byte
	// stages a validator for disabling next flag ledger; nil if none
	ToDisablePending *[33]byte
	// stages a validator for re-enabling next flag ledger; nil if none
	ToReEnablePending *[33]byte
}

// effectiveNegUNL applies State's pending changes to yield the negUNL the
// upcoming flag ledger will see.
func (s State) effectiveNegUNL() map[[33]byte]struct{} {
	out := make(map[[33]byte]struct{}, len(s.DisabledKeys)+1)
	for _, k := range s.DisabledKeys {
		out[k] = struct{}{}
	}
	if s.ToDisablePending != nil {
		out[*s.ToDisablePending] = struct{}{}
	}
	if s.ToReEnablePending != nil {
		delete(out, *s.ToReEnablePending)
	}
	return out
}

// Voter is the producer state: the local node ID and the new-validator
// skip table, which must persist across rounds (newly-trusted validators
// are exempt from ToDisable for NewValidatorDisableSkip ledgers). The mutex
// guards that table; methods are safe for concurrent use.
type Voter struct {
	myID consensus.NodeID

	mu            sync.Mutex
	newValidators map[consensus.NodeID]uint32 // ledger seq when added
}

// NewVoter constructs a Voter; myID is the 20-byte NodeID that also keys
// the score table.
func NewVoter(myID consensus.NodeID) *Voter {
	return &Voter{
		myID:          myID,
		newValidators: make(map[consensus.NodeID]uint32),
	}
}

// MyID returns the local NodeID, exposed so callers can look up their own
// score before invoking DoVoting.
func (v *Voter) MyID() consensus.NodeID {
	return v.myID
}

// NewValidators registers newly-trusted validators at seq, exempting them
// from ToDisable for the next NewValidatorDisableSkip ledgers.
func (v *Voter) NewValidators(seq uint32, nowTrusted []consensus.NodeID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, n := range nowTrusted {
		if _, ok := v.newValidators[n]; !ok {
			v.newValidators[n] = seq
		}
	}
}

// PurgeNewValidators drops entries older than NewValidatorDisableSkip
// ledgers relative to seq.
func (v *Voter) PurgeNewValidators(seq uint32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for n, addedSeq := range v.newValidators {
		if seq-addedSeq > NewValidatorDisableSkip {
			delete(v.newValidators, n)
		}
	}
}

// keyToNodeID derives the 20-byte NodeID from a 33-byte master pubkey. The
// pubkey travels on the wire (sfUNLModifyValidator) while the score table
// is NodeID-keyed, so Go and rippled validators converge on the same pick.
func keyToNodeID(k [33]byte) consensus.NodeID {
	return consensus.CalcNodeID(k)
}

// DoVoting runs the producer end-to-end and returns the UNLModify blobs to
// inject (at most one ToDisable plus one ToReEnable). The upcoming ledger is
// prevLedgerSeq + 1; prevLedgerHash is the deterministic pad for picking.
// Returns nil when there's nothing to vote (insufficient local participation
// or no candidates).
//
// scoreTable contract: callers may pass any table; DoVoting restricts it to
// the UNL (missing UNL keys score 0, non-UNL keys dropped), so no pre-fill or
// pre-filter is needed.
func (v *Voter) DoVoting(
	prevLedgerSeq uint32,
	prevLedgerHash [32]byte,
	unlKeys [][33]byte,
	state State,
	scoreTable map[consensus.NodeID]uint32,
) ([][]byte, error) {
	// Build the trusted-key index once.
	unlNodeIDs := make(map[consensus.NodeID][33]byte, len(unlKeys))
	for _, k := range unlKeys {
		unlNodeIDs[keyToNodeID(k)] = k
	}

	// Restrict the score table to the UNL (each missing key 0): a non-UNL
	// stray could become a phantom ToDisable candidate that forks the vote
	// or aborts the round. Local copy so the caller's map isn't mutated.
	filledScoreTable := make(map[consensus.NodeID]uint32, len(unlNodeIDs))
	for n := range unlNodeIDs {
		filledScoreTable[n] = scoreTable[n]
	}

	// Refuse to vote if local participation is insufficient. The <= gate is
	// exact: == MinLocalValsToVote is also a no-vote (rippled's else-if uses
	// strict >). A count above the window is impossible → surface the error.
	myCount := filledScoreTable[v.myID]
	if myCount <= MinLocalValsToVote {
		return nil, nil
	}
	if myCount > protocol.FlagLedgerInterval {
		return nil, fmt.Errorf("%w: %d > %d", ErrLocalCountExceedsWindow, myCount, protocol.FlagLedgerInterval)
	}

	// effective negUNL for the upcoming flag ledger (current ± pending)
	negUnlKeys := state.effectiveNegUNL()
	negUnlNodeIDs := make(map[consensus.NodeID]struct{}, len(negUnlKeys))
	keyByNode := make(map[consensus.NodeID][33]byte, len(unlKeys)+len(negUnlKeys))
	maps.Copy(keyByNode, unlNodeIDs)
	for k := range negUnlKeys {
		nid := keyToNodeID(k)
		negUnlNodeIDs[nid] = struct{}{}
		if _, ok := keyByNode[nid]; !ok {
			keyByNode[nid] = k
		}
	}

	upcomingSeq := prevLedgerSeq + 1
	v.PurgeNewValidators(upcomingSeq)

	candidates := v.findAllCandidates(unlNodeIDs, negUnlNodeIDs, filledScoreTable)

	var blobs [][]byte
	if len(candidates.toDisable) > 0 {
		picked := choose(prevLedgerHash, candidates.toDisable)
		key, ok := keyByNode[picked]
		if !ok {
			return nil, fmt.Errorf("negativeunlvote: picked toDisable candidate has no master key in lookup table")
		}
		blob, err := buildUNLModifyTx(upcomingSeq, key, ToDisable)
		if err != nil {
			return nil, fmt.Errorf("negativeunlvote: serialize toDisable: %w", err)
		}
		blobs = append(blobs, blob)
	}
	if len(candidates.toReEnable) > 0 {
		picked := choose(prevLedgerHash, candidates.toReEnable)
		key, ok := keyByNode[picked]
		if !ok {
			return nil, fmt.Errorf("negativeunlvote: picked toReEnable candidate has no master key in lookup table")
		}
		blob, err := buildUNLModifyTx(upcomingSeq, key, ToReEnable)
		if err != nil {
			return nil, fmt.Errorf("negativeunlvote: serialize toReEnable: %w", err)
		}
		blobs = append(blobs, blob)
	}

	return blobs, nil
}

type candidateSet struct {
	toDisable  []consensus.NodeID
	toReEnable []consensus.NodeID
}

// findAllCandidates turns the score table into candidates: 25% cap,
// new-validator skip, and the left-the-UNL ToReEnable fallback.
func (v *Voter) findAllCandidates(
	unl map[consensus.NodeID][33]byte,
	negUNL map[consensus.NodeID]struct{},
	scoreTable map[consensus.NodeID]uint32,
) candidateSet {
	maxListed := int(math.Ceil(float64(len(unl)) * MaxListedFraction))
	listed := 0
	for n := range unl {
		if _, ok := negUNL[n]; ok {
			listed++
		}
	}
	canAdd := listed < maxListed

	v.mu.Lock()
	defer v.mu.Unlock()

	var c candidateSet
	for nodeID, score := range scoreTable {
		_, isNegUNL := negUNL[nodeID]
		_, isNew := v.newValidators[nodeID]

		if canAdd && score < LowWaterMark && !isNegUNL && !isNew {
			c.toDisable = append(c.toDisable, nodeID)
		}
		if score > HighWaterMark && isNegUNL {
			c.toReEnable = append(c.toReEnable, nodeID)
		}
	}

	// Fallback (only when no score-driven re-enable): re-enable a disabled
	// validator that left the UNL entirely.
	if len(c.toReEnable) == 0 {
		for n := range negUNL {
			if _, inUNL := unl[n]; !inUNL {
				c.toReEnable = append(c.toReEnable, n)
			}
		}
	}

	return c
}

// choose deterministically picks one NodeID by XORing each against the
// prevLedger hash pad and taking the minimum, so every validator converges
// without coordination. NodeID is already rippled's 20-byte digest, so the
// XOR is direct (no rehash) and matches rippled byte-for-byte.
func choose(randomPad [32]byte, candidates []consensus.NodeID) consensus.NodeID {
	if len(candidates) == 0 {
		var zero consensus.NodeID
		return zero
	}
	best := candidates[0]
	bestKey := xorCalcNodeID(best, randomPad)
	for i := 1; i < len(candidates); i++ {
		k := xorCalcNodeID(candidates[i], randomPad)
		if compareNodeID20(k, bestKey) < 0 {
			best = candidates[i]
			bestKey = k
		}
	}
	return best
}

// xorCalcNodeID computes NodeID ^ randomPad[:20].
func xorCalcNodeID(n consensus.NodeID, pad [32]byte) [20]byte {
	var out [20]byte
	for i := range 20 {
		out[i] = n[i] ^ pad[i]
	}
	return out
}

func compareNodeID20(a, b [20]byte) int {
	for i := range 20 {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// buildUNLModifyTx serializes a UNLModify pseudo-tx: zero account/fee/key,
// sequence 0.
func buildUNLModifyTx(seq uint32, validator [33]byte, modify Modify) ([]byte, error) {
	disabling := uint8(modify)
	return common.BuildPseudoTx(tx.TypeUNLModify, func(base tx.BaseTx) tx.Transaction {
		return &pseudo.UNLModify{
			BaseTx:             base,
			UNLModifyDisabling: &disabling,
			LedgerSequence:     &seq,
			UNLModifyValidator: hex.EncodeToString(validator[:]),
		}
	})
}
