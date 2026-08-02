package adaptor

import (
	"errors"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/negativeunlvote"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// GenerateNegativeUNLPseudoTx produces the UNLModify pseudo-tx blobs
// to inject on a NegativeUNL-enabled voting ledger: read the parent
// NegativeUNL SLE, build the score table from the last
// FlagLedgerInterval validation snapshots, delegate to
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
//   - local participation is below the strict voting threshold —
//     local view too narrow to trust
//   - no candidates qualify
//
// These normal zero-vote paths are silent. Exact-threshold and above-window
// counts are surfaced and logged because rippled treats both as error
// diagnostics while producing no vote.
func (a *Adaptor) GenerateNegativeUNLPseudoTx(prev consensus.Ledger) [][]byte {
	a.trustUpdateMu.Lock()
	a.mu.Lock()
	voter := a.negUNLVoter
	historian := a.validationHistorian
	masterKeys := a.trustedMasterKeys
	a.mu.Unlock()
	a.trustUpdateMu.Unlock()

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

	scoreTable, ok := a.buildNegativeUNLScoreTable(concrete, historian)
	if !ok {
		return nil
	}

	unlKeys := make([][33]byte, len(masterKeys))
	copy(unlKeys, masterKeys)

	prevSeq := concrete.Sequence()
	prevHash := concrete.Hash()

	// DoVoting owns the new-validator purge and the local-participation
	// gate; don't duplicate either here.
	blobs, err := voter.DoVoting(prevSeq, prevHash, unlKeys, state, scoreTable)
	if err != nil {
		a.logNegativeUNLVoteError(prevSeq, err)
		return nil
	}
	return blobs
}

func (a *Adaptor) logNegativeUNLVoteError(prevSeq uint32, err error) {
	switch {
	case errors.Is(err, negativeunlvote.ErrLocalCountAtThreshold):
		a.logger.Error("NegativeUNL: local validation count equals strict voting threshold; abstaining",
			"prev_seq", prevSeq,
			"err", err,
		)
	case errors.Is(err, negativeunlvote.ErrLocalCountExceedsWindow):
		a.logger.Error("NegativeUNL: local validation count exceeds window — likely duplicate-validation bug",
			"prev_seq", prevSeq,
			"err", err,
		)
	default:
		a.logger.Warn("NegativeUNL: DoVoting failed",
			"prev_seq", prevSeq,
			"err", err,
		)
	}
}

// OnUNLChange registers validators newly added to the operator-trusted
// set with the NegativeUNL voter's grace-period table. `upcomingSeq` is
// the sequence of the round being started (i.e. `prevLedger.Seq() + 1`);
// `nowTrusted` is the *delta* — validators added since the previous
// round, NOT the full UNL. The caller (engine.startRoundLocked) owns
// the feature-gate and delta computation, so the gate and the
// `nowTrusted` set both originate outside the voter.
//
// Does NOT purge — purge is owned by the voting path inside
// GenerateNegativeUNLPseudoTx. Calling it here would double-run the
// purge once the engine drives this per round.
//
// No-op when `nowTrusted` is empty, or when the adaptor was constructed
// without a Voter (no identity or master keys plumbed in — fixtures,
// observer nodes).
func (a *Adaptor) OnUNLChange(upcomingSeq uint32, nowTrusted []consensus.NodeID) {
	if len(nowTrusted) == 0 {
		return
	}
	a.mu.Lock()
	voter := a.negUNLVoter
	a.mu.Unlock()
	if voter == nil {
		return
	}
	voter.NewValidators(upcomingSeq, nowTrusted)
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
			if len(v.PublicKey) != 33 {
				continue
			}
			var k [33]byte
			copy(k[:], v.PublicKey)
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

// buildNegativeUNLScoreTable builds the per-validator score table:
//
//  1. Read the parent ledger's rolling skip-list of the previous
//     FlagLedgerInterval ledger hashes.
//  2. For each ancestor, look up the trusted validations that named
//     it and increment a per-NodeID counter.
//
// Returns (nil, false) only when the parent's skip-list is shorter
// than FlagLedgerInterval (early ledgers near genesis).
// The local-participation gate is enforced by DoVoting. It abstains on low
// participation and surfaces exact-threshold and above-window diagnostics.
// DoVoting also restricts this table to the UNL, so an over-populated
// table (NodeIDs that have since left the UNL) is harmless.
func (a *Adaptor) buildNegativeUNLScoreTable(
	prev interface {
		Sequence() uint32
		SkipListHashes() ([][32]byte, error)
	},
	historian consensus.ValidationHistorian,
) (map[consensus.NodeID]uint32, bool) {
	// Pin the window this vote scans so a concurrent ExpireOld can't drop its
	// low end. rippled keeps [seq-1, seq+interval) for the forward window
	// against its time-based expiry; go-xrpl expires by sequence, so we also
	// widen the pin down over the ancestors we read, and pin before the
	// skip-list check so it holds on a round we can't vote. seq = prevSeq + 1;
	// the low end saturates to avoid underflow at the first flag ledger.
	upcoming := prev.Sequence() + 1
	var keepLow uint32
	if upcoming >= 1+protocol.FlagLedgerInterval {
		keepLow = upcoming - 1 - protocol.FlagLedgerInterval
	}
	historian.SetSeqToKeep(keepLow, upcoming+protocol.FlagLedgerInterval)

	ancestors, err := prev.SkipListHashes()
	if err != nil || uint32(len(ancestors)) < protocol.FlagLedgerInterval {
		return nil, false
	}

	n := uint32(len(ancestors))
	window := protocol.FlagLedgerInterval
	scoreTable := make(map[consensus.NodeID]uint32)
	for i := range window {
		ledgerHash := ancestors[n-1-i]
		// The skip-list entry at this position is the ledger at
		// (upcoming-2-i), matching rippled's getTrustedForLedger(hash, seq).
		ledgerSeq := upcoming - 2 - i
		for _, v := range historian.GetTrustedFullValidations(consensus.LedgerID(ledgerHash), ledgerSeq) {
			if v == nil || !v.Full || v.LedgerSeq != ledgerSeq {
				continue
			}
			scoreTable[v.NodeID]++
		}
	}
	return scoreTable, true
}
