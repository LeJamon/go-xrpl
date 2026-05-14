package adaptor

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/negativeunlvote"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
)

// GenerateNegativeUNLPseudoTx produces the UNLModify pseudo-tx blobs
// to inject on a NegativeUNL-enabled voting ledger. Mirrors rippled's
// NegativeUNLVote::doVoting (NegativeUNLVote.cpp:36-108) wired through
// the adaptor: read the parent NegativeUNL SLE, build the score table
// from the last FlagLedgerInterval validation snapshots, delegate to
// negativeunlvote.Voter for candidate selection, and return the
// serialized [][]byte (zero, one, or two blobs).
//
// Returns nil (no votes) under any of:
//   - this adaptor was constructed without a Voter (no identity or
//     master keys plumbed in — fixtures, observer nodes)
//   - the ValidationHistorian has not yet been wired by the engine
//   - prevLedger is not a *LedgerWrapper (test ledger types)
//   - the skip-list contains fewer than FlagLedgerInterval ancestors
//     (early ledgers near genesis)
//   - local participation is below MinLocalValsToVote (230/256) —
//     local view too narrow to trust
//   - no candidates qualify
//
// All zero-vote paths are silent; an operator running a validator on
// a NegativeUNL-enabled network sees neither errors nor warnings when
// the round simply has nothing to do.
func (a *Adaptor) GenerateNegativeUNLPseudoTx(prev consensus.Ledger) [][]byte {
	a.mu.Lock()
	voter := a.negUNLVoter
	historian := a.validationHistorian
	masterKeys := a.trustedMasterKeys
	a.mu.Unlock()

	if voter == nil || historian == nil || len(masterKeys) == 0 {
		return nil
	}

	wrapper, ok := prev.(*LedgerWrapper)
	if !ok {
		return nil
	}
	concrete := wrapper.Unwrap()
	if concrete == nil {
		return nil
	}

	state, err := a.negativeUNLState(concrete)
	if err != nil {
		a.logger.Warn("NegativeUNL: parse SLE failed; skipping vote this round",
			"prev_seq", concrete.Sequence(),
			"err", err,
		)
		return nil
	}

	scoreTable, ok := a.buildNegativeUNLScoreTable(concrete, historian, voter.MyID())
	if !ok {
		return nil
	}

	unlKeys := make([][33]byte, len(masterKeys))
	copy(unlKeys, masterKeys)

	prevSeq := concrete.Sequence()
	prevHash := concrete.Hash()

	voter.PurgeNewValidators(prevSeq + 1)

	blobs, err := voter.DoVoting(prevSeq, prevHash, unlKeys, state, scoreTable)
	if err != nil {
		if errors.Is(err, negativeunlvote.ErrLocalCountExceedsWindow) {
			a.logger.Error("NegativeUNL: local validation count exceeds window — likely duplicate-validation bug",
				"prev_seq", prevSeq,
				"err", err,
			)
			return nil
		}
		a.logger.Warn("NegativeUNL: DoVoting failed",
			"prev_seq", prevSeq,
			"err", err,
		)
		return nil
	}
	return blobs
}

// negativeUNLState reads and parses the NegativeUNL SLE from the
// given ledger into the negativeunlvote.State shape. An empty/absent
// SLE returns the zero-value State (no disabled, no pending changes).
func (a *Adaptor) negativeUNLState(l interface {
	Read(keylet.Keylet) ([]byte, error)
}) (negativeunlvote.State, error) {
	raw, err := l.Read(keylet.NegativeUNL())
	if err != nil {
		return negativeunlvote.State{}, err
	}
	parsed, err := pseudo.ParseNegativeUNLSLE(raw)
	if err != nil {
		return negativeunlvote.State{}, err
	}
	state := negativeunlvote.State{}
	if n := len(parsed.DisabledValidators); n > 0 {
		state.DisabledKeys = make([][33]byte, 0, n)
		for _, v := range parsed.DisabledValidators {
			if len(v) != 33 {
				continue
			}
			var k [33]byte
			copy(k[:], v)
			state.DisabledKeys = append(state.DisabledKeys, k)
		}
	}
	if len(parsed.ValidatorToDisable) == 33 {
		var k [33]byte
		copy(k[:], parsed.ValidatorToDisable)
		state.ToDisablePending = &k
	}
	if len(parsed.ValidatorToReEnable) == 33 {
		var k [33]byte
		copy(k[:], parsed.ValidatorToReEnable)
		state.ToReEnablePending = &k
	}
	return state, nil
}

// buildNegativeUNLScoreTable mirrors rippled's
// NegativeUNLVote::buildScoreTable (NegativeUNLVote.cpp:173-244):
//
//  1. Read the parent ledger's rolling skip-list of the previous
//     FlagLedgerInterval ledger hashes.
//  2. For each ancestor, look up the trusted validations that named
//     it and increment a per-NodeID counter.
//  3. Enforce the local-node participation gate
//     ([MinLocalValsToVote, FlagLedgerInterval]) — outside that range
//     return ok=false so the producer abstains.
//
// Returns (nil, false) when the parent's skip-list is shorter than
// FlagLedgerInterval (early ledgers) or when local participation is
// out of range. A successful return guarantees every trusted
// validator the Voter consults will fall back to score 0 (rippled's
// invariant at NegativeUNLVote.cpp:197-200, enforced inside DoVoting).
func (a *Adaptor) buildNegativeUNLScoreTable(
	prev interface {
		SkipListHashes() ([][32]byte, error)
	},
	historian consensus.ValidationHistorian,
	myID consensus.NodeID,
) (map[consensus.NodeID]uint32, bool) {
	ancestors, err := prev.SkipListHashes()
	if err != nil || uint32(len(ancestors)) < consensus.FlagLedgerInterval {
		return nil, false
	}

	n := uint32(len(ancestors))
	window := consensus.FlagLedgerInterval
	scoreTable := make(map[consensus.NodeID]uint32)
	for i := uint32(0); i < window; i++ {
		ledgerHash := ancestors[n-1-i]
		for _, v := range historian.GetTrustedValidations(consensus.LedgerID(ledgerHash)) {
			scoreTable[v.NodeID]++
		}
	}

	myCount := scoreTable[myID]
	if myCount < negativeunlvote.MinLocalValsToVote || myCount > window {
		return nil, false
	}
	return scoreTable, true
}
