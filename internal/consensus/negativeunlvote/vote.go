// Package negativeunlvote ports rippled's NegativeUNLVote
// (src/xrpld/app/misc/NegativeUNLVote.{h,cpp}) — the producer side
// that decides whether to inject a UNLModify pseudo-tx into the
// consensus tx set on a flag-ledger boundary, based on per-validator
// participation in the last FlagLedgerInterval ledgers.
//
// The algorithm:
//
//  1. Build a reliability score table — for each trusted validator,
//     count its validations across the last FlagLedgerInterval (256)
//     ledgers, indexed by the parent ledger's skip list. Refuse to
//     vote if our local node's count is below MinLocalValsToVote
//     (the local view is too narrow to trust).
//
//  2. Find candidates using the (low|high)-water-mark thresholds and
//     a 25% cap on listed validators. ToDisable picks unreliable
//     validators not already on the negUNL; ToReEnable picks recovered
//     validators currently on the negUNL.
//
//  3. Pick at most one candidate per category, deterministically by
//     XOR with prevLedger.hash so every validator on the network
//     converges on the same choice without coordination.
//
//  4. Serialize each picked candidate as a UNLModify pseudo-tx.
package negativeunlvote

import (
	"encoding/hex"
	"fmt"
	"math"
	"sync"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
)

const (
	// flagLedgerInterval is the period (in ledgers) the producer
	// scores validators over. Mirrors consensus.FlagLedgerInterval;
	// duplicated here as a uint32 constant so the threshold
	// constants below can be evaluated at compile time.
	flagLedgerInterval uint32 = 256

	// LowWaterMark is the validation-count threshold below which a
	// trusted validator is considered unreliable and becomes a
	// ToDisable candidate. Matches rippled's
	// negativeUNLLowWaterMark = FLAG_LEDGER_INTERVAL * 50 / 100.
	LowWaterMark uint32 = flagLedgerInterval * 50 / 100

	// HighWaterMark is the validation-count threshold above which a
	// currently-disabled validator becomes a ToReEnable candidate.
	// Matches rippled's negativeUNLHighWaterMark =
	// FLAG_LEDGER_INTERVAL * 80 / 100.
	HighWaterMark uint32 = flagLedgerInterval * 80 / 100

	// MinLocalValsToVote is the minimum number of validations the
	// local node itself must have produced over the score-table
	// window for the local view to be considered reliable. Below
	// this threshold the producer refuses to vote — its local
	// reliability measurement could be wrong. Matches rippled's
	// negativeUNLMinLocalValsToVote = FLAG_LEDGER_INTERVAL * 90 / 100.
	MinLocalValsToVote uint32 = flagLedgerInterval * 90 / 100

	// NewValidatorDisableSkip is the number of ledgers a freshly-
	// added validator is exempt from ToDisable voting. Matches
	// rippled's newValidatorDisableSkip = FLAG_LEDGER_INTERVAL * 2.
	NewValidatorDisableSkip uint32 = flagLedgerInterval * 2

	// MaxListedFraction caps the proportion of the UNL that may
	// appear on the NegativeUNL at any one time. ToDisable
	// candidates are dropped once this cap is reached. Matches
	// rippled's negativeUNLMaxListed = 0.25.
	MaxListedFraction float64 = 0.25
)

// Modify identifies the direction of a UNLModify pseudo-tx.
type Modify uint8

const (
	ToDisable  Modify = 1 // UNLModify with sfUNLModifyDisabling=1
	ToReEnable Modify = 0 // UNLModify with sfUNLModifyDisabling=0
)

// State captures the NegativeUNL ledger entry of the parent ledger:
// the currently-disabled set plus any pending change a previous
// flag-ledger UNLModify staged but that hasn't taken effect yet.
//
// Mirrors prevLedger->negativeUNL() / validatorToDisable() /
// validatorToReEnable() at NegativeUNLVote.cpp:61-78.
type State struct {
	// DisabledKeys are the master pubkeys currently on the
	// negUNL — i.e. excluded from quorum.
	DisabledKeys [][33]byte
	// ToDisablePending stages a validator for disabling on the
	// upcoming flag ledger. Nil when no change is pending.
	ToDisablePending *[33]byte
	// ToReEnablePending stages a validator for re-enabling on the
	// upcoming flag ledger. Nil when no change is pending.
	ToReEnablePending *[33]byte
}

// effectiveNegUNL applies the pending changes from State to produce
// the negUNL the upcoming flag ledger will see. Mirrors the
// negUnlKeys.insert / negUnlKeys.erase handling at
// NegativeUNLVote.cpp:61-67.
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

// Voter is the producer state. Holds the local node's identifier and
// the new-validator skip table. Methods are safe for concurrent use.
type Voter struct {
	myID consensus.NodeID

	mu            sync.Mutex
	newValidators map[consensus.NodeID]uint32 // ledger seq when added
}

// NewVoter constructs a Voter for the local node. myID must be the
// 33-byte master pubkey representation goXRPL uses for NodeID — the
// same value that appears in scoreTable keys.
func NewVoter(myID consensus.NodeID) *Voter {
	return &Voter{
		myID:          myID,
		newValidators: make(map[consensus.NodeID]uint32),
	}
}

// NewValidators registers a set of newly-trusted validators at the
// given ledger sequence so they are exempt from ToDisable voting for
// the next NewValidatorDisableSkip ledgers. Mirrors
// NegativeUNLVote.cpp:322-337.
func (v *Voter) NewValidators(seq uint32, nowTrusted []consensus.NodeID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, n := range nowTrusted {
		if _, ok := v.newValidators[n]; !ok {
			v.newValidators[n] = seq
		}
	}
}

// PurgeNewValidators removes any new-validator entry that is older
// than NewValidatorDisableSkip ledgers relative to seq. Mirrors
// NegativeUNLVote.cpp:339-355.
func (v *Voter) PurgeNewValidators(seq uint32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for n, addedSeq := range v.newValidators {
		if seq-addedSeq > NewValidatorDisableSkip {
			delete(v.newValidators, n)
		}
	}
}

// keyToNodeID is the local node-id derivation goXRPL uses today. It
// mirrors the layout of consensus.NodeID (33-byte compressed pubkey)
// rather than rippled's 20-byte RIPEMD-160 hash. Cross-implementation
// pseudo-tx parity is unaffected because the chosen candidate (the
// pubkey) is what ends up in sfUNLModifyValidator on the wire — the
// NodeID is only used as the score-table key locally.
func keyToNodeID(k [33]byte) consensus.NodeID {
	return consensus.NodeID(k)
}

// DoVoting runs the producer end-to-end and returns the serialized
// UNLModify pseudo-tx blobs to inject (zero, one, or two — at most
// one ToDisable plus at most one ToReEnable). prevLedgerSeq is the
// sequence of the parent ledger; the upcoming ledger is therefore
// prevLedgerSeq + 1. prevLedgerHash is used as the deterministic
// random pad for candidate picking. unlKeys lists the trusted
// validator master keys; state describes the parent ledger's
// NegativeUNL SLE; scoreTable maps each trusted validator's NodeID
// to its validation count over the last FlagLedgerInterval ledgers.
//
// Returns nil when no pseudo-tx is needed or when the producer
// chooses not to vote (insufficient local participation, no
// candidates). Errors from pseudo-tx serialization are returned to
// the caller; the engine treats a non-nil error as a producer
// failure and falls through to no-injection rather than blocking the
// round.
func (v *Voter) DoVoting(
	prevLedgerSeq uint32,
	prevLedgerHash [32]byte,
	unlKeys [][33]byte,
	state State,
	scoreTable map[consensus.NodeID]uint32,
) ([][]byte, error) {
	// Refuse to vote if local participation is insufficient. See
	// buildScoreTable's myValidationCount check at
	// NegativeUNLVote.cpp:216-244.
	myCount := scoreTable[v.myID]
	if myCount < MinLocalValsToVote {
		return nil, nil
	}

	// Cannot exceed FlagLedgerInterval validations in the window —
	// rippled treats this as a logic error and refuses to vote.
	if myCount > flagLedgerInterval {
		return nil, fmt.Errorf("negativeunlvote: local validation count %d exceeds window %d", myCount, flagLedgerInterval)
	}

	// Build the trusted-key index once.
	unlNodeIDs := make(map[consensus.NodeID][33]byte, len(unlKeys))
	for _, k := range unlKeys {
		unlNodeIDs[keyToNodeID(k)] = k
	}

	// Resolve the effective negUNL for the upcoming flag ledger
	// (current set ± any pending change).
	negUnlKeys := state.effectiveNegUNL()
	negUnlNodeIDs := make(map[consensus.NodeID]struct{}, len(negUnlKeys))
	keyByNode := make(map[consensus.NodeID][33]byte, len(unlKeys)+len(negUnlKeys))
	for n, k := range unlNodeIDs {
		keyByNode[n] = k
	}
	for k := range negUnlKeys {
		nid := keyToNodeID(k)
		negUnlNodeIDs[nid] = struct{}{}
		if _, ok := keyByNode[nid]; !ok {
			keyByNode[nid] = k
		}
	}

	upcomingSeq := prevLedgerSeq + 1
	v.PurgeNewValidators(upcomingSeq)

	candidates := v.findAllCandidates(unlNodeIDs, negUnlNodeIDs, scoreTable)

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

type candidates struct {
	toDisable  []consensus.NodeID
	toReEnable []consensus.NodeID
}

// findAllCandidates is the score-table → candidate-set step. Mirrors
// NegativeUNLVote.cpp:247-320 including the 25% cap, the
// new-validator skip, and the "drop validators that left the UNL"
// fallback for ToReEnable.
func (v *Voter) findAllCandidates(
	unl map[consensus.NodeID][33]byte,
	negUNL map[consensus.NodeID]struct{},
	scoreTable map[consensus.NodeID]uint32,
) candidates {
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

	var c candidates
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

	// Fallback: if a previously-disabled validator has been removed
	// from the UNL entirely, re-enable it on the negUNL since it's
	// no longer eligible. Only consulted when the score-driven
	// re-enable list is empty (NegativeUNLVote.cpp:309-318).
	if len(c.toReEnable) == 0 {
		for n := range negUNL {
			if _, inUNL := unl[n]; !inUNL {
				c.toReEnable = append(c.toReEnable, n)
			}
		}
	}

	return c
}

// choose deterministically picks one NodeID from candidates using
// the prevLedger hash as the random pad. Mirrors
// NegativeUNLVote.cpp:142-161 — XOR with the pad and pick the
// minimum. This converges every validator on the same choice
// without coordination.
//
// goXRPL's NodeID is 33 bytes vs rippled's 20; we XOR over the
// first 32 bytes (matching the hash-pad width) and treat them as a
// big-endian comparison key.
func choose(randomPad [32]byte, candidates []consensus.NodeID) consensus.NodeID {
	if len(candidates) == 0 {
		var zero consensus.NodeID
		return zero
	}
	best := candidates[0]
	bestKey := xorKey(best, randomPad)
	for i := 1; i < len(candidates); i++ {
		k := xorKey(candidates[i], randomPad)
		if compareKey(k, bestKey) < 0 {
			best = candidates[i]
			bestKey = k
		}
	}
	return best
}

func xorKey(n consensus.NodeID, pad [32]byte) [32]byte {
	var out [32]byte
	for i := 0; i < 32; i++ {
		out[i] = n[i] ^ pad[i]
	}
	return out
}

func compareKey(a, b [32]byte) int {
	for i := 0; i < 32; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// zeroAccount is the base58-encoded all-zero AccountID used as the
// source account on every XRPL pseudo-transaction (rippled
// AccountID()). The wire form serializes to a 20-byte zero blob.
const zeroAccount = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// buildUNLModifyTx serializes a UNLModify pseudo-tx for inclusion in
// the proposal initial set. Wire format mirrors rippled's
// NegativeUNLVote::addTx at NegativeUNLVote.cpp:110-140 — zero
// account, zero fee, empty signing pubkey, sequence 0.
func buildUNLModifyTx(seq uint32, validator [33]byte, modify Modify) ([]byte, error) {
	disabling := uint8(modify)
	zeroSeq := uint32(0)
	utx := &pseudo.UNLModify{
		BaseTx:             *tx.NewBaseTx(tx.TypeUNLModify, zeroAccount),
		UNLModifyDisabling: &disabling,
		LedgerSequence:     &seq,
		UNLModifyValidator: hex.EncodeToString(validator[:]),
	}
	utx.Common.Fee = "0"
	utx.Common.SigningPubKey = ""
	utx.Common.Sequence = &zeroSeq

	flat, err := utx.Flatten()
	if err != nil {
		return nil, fmt.Errorf("flatten UNLModify: %w", err)
	}
	hexStr, err := binarycodec.Encode(flat)
	if err != nil {
		return nil, fmt.Errorf("encode UNLModify: %w", err)
	}
	return hex.DecodeString(hexStr)
}
