// Package negativeunlvote produces UNLModify pseudo-transactions for flag ledgers.
package negativeunlvote

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/common"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/protocol"
)

var (
	// ErrLocalCountAtThreshold signals the strict voting threshold was met but
	// not exceeded. The caller abstains and surfaces the diagnostic.
	ErrLocalCountAtThreshold = errors.New("negativeunlvote: local validation count equals voting threshold")
	// ErrLocalCountExceedsWindow signals an impossible validation count.
	ErrLocalCountExceedsWindow = errors.New("negativeunlvote: local validation count exceeds flag-ledger window")
)

const (
	lowWaterMark            uint32 = protocol.FlagLedgerInterval * 50 / 100
	highWaterMark           uint32 = protocol.FlagLedgerInterval * 80 / 100
	minLocalValsToVote      uint32 = protocol.FlagLedgerInterval * 90 / 100
	newValidatorDisableSkip uint32 = protocol.FlagLedgerInterval * 2
)

type modify uint8

const (
	toReEnable modify = iota
	toDisable
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

// Voter retains the local identity and new-validator grace periods across rounds.
type Voter struct {
	myID consensus.NodeID

	mu            sync.Mutex
	newValidators map[consensus.NodeID]uint32
}

func NewVoter(myID consensus.NodeID) *Voter {
	return &Voter{
		myID:          myID,
		newValidators: make(map[consensus.NodeID]uint32),
	}
}

// NewValidators registers newly trusted validators for the disable grace period.
func (v *Voter) NewValidators(seq uint32, nowTrusted []consensus.NodeID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, n := range nowTrusted {
		if _, ok := v.newValidators[n]; !ok {
			v.newValidators[n] = seq
		}
	}
}

func (v *Voter) purgeNewValidators(seq uint32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for n, addedSeq := range v.newValidators {
		if seq-addedSeq > newValidatorDisableSkip {
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
// Counts below the participation threshold and rounds without candidates return
// nil without an error. Rejected local counts return nil with a diagnostic error.
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

	// Counts below the threshold abstain normally. The exact threshold and an
	// impossible above-window count retain rippled's error observability.
	myCount := filledScoreTable[v.myID]
	if myCount < minLocalValsToVote {
		return nil, nil
	}
	if myCount == minLocalValsToVote {
		return nil, fmt.Errorf("%w: %d", ErrLocalCountAtThreshold, myCount)
	}
	if myCount > protocol.FlagLedgerInterval {
		return nil, fmt.Errorf("%w: %d > %d", ErrLocalCountExceedsWindow, myCount, protocol.FlagLedgerInterval)
	}

	negUnlKeys := state.effectiveNegUNL()
	negUnlNodeIDs := make(map[consensus.NodeID]struct{}, len(negUnlKeys))
	// Every candidate comes from the UNL or effective NegativeUNL, both indexed here.
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
	v.purgeNewValidators(upcomingSeq)

	candidates := v.findAllCandidates(unlNodeIDs, negUnlNodeIDs, filledScoreTable)

	var blobs [][]byte
	if len(candidates.toDisable) > 0 {
		key := keyByNode[choose(prevLedgerHash, candidates.toDisable)]
		blob, err := buildUNLModifyTx(upcomingSeq, key, toDisable)
		if err != nil {
			return nil, fmt.Errorf("negativeunlvote: serialize toDisable: %w", err)
		}
		blobs = append(blobs, blob)
	}
	if len(candidates.toReEnable) > 0 {
		key := keyByNode[choose(prevLedgerHash, candidates.toReEnable)]
		blob, err := buildUNLModifyTx(upcomingSeq, key, toReEnable)
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

func (v *Voter) findAllCandidates(
	unl map[consensus.NodeID][33]byte,
	negUNL map[consensus.NodeID]struct{},
	scoreTable map[consensus.NodeID]uint32,
) candidateSet {
	maxListed := (len(unl) + 3) / 4
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

		if canAdd && score < lowWaterMark && !isNegUNL && !isNew {
			c.toDisable = append(c.toDisable, nodeID)
		}
		if score > highWaterMark && isNegUNL {
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
	best := candidates[0]
	bestKey := xorCalcNodeID(best, randomPad)
	for i := 1; i < len(candidates); i++ {
		k := xorCalcNodeID(candidates[i], randomPad)
		if bytes.Compare(k[:], bestKey[:]) < 0 {
			best = candidates[i]
			bestKey = k
		}
	}
	return best
}

func xorCalcNodeID(n consensus.NodeID, pad [32]byte) [20]byte {
	var out [20]byte
	for i := range 20 {
		out[i] = n[i] ^ pad[i]
	}
	return out
}

func buildUNLModifyTx(seq uint32, validator [33]byte, modify modify) ([]byte, error) {
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
