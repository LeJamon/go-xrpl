package amm

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// AMMVote votes on the trading fee for an AMM.
type AMMVote struct {
	tx.BaseTx

	// Asset identifies the first asset of the AMM (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Asset2 identifies the second asset of the AMM (required)
	Asset2 tx.Asset `json:"Asset2" xrpl:"Asset2,asset"`

	// TradingFee is the proposed fee in basis points (0-1000)
	TradingFee uint16 `json:"TradingFee" xrpl:"TradingFee"`
}

// NewAMMVote creates a new AMMVote transaction
func NewAMMVote(account string, asset, asset2 tx.Asset, tradingFee uint16) *AMMVote {
	return &AMMVote{
		BaseTx:     *tx.NewBaseTx(tx.TypeAMMVote, account),
		Asset:      asset,
		Asset2:     asset2,
		TradingFee: tradingFee,
	}
}

func (a *AMMVote) TxType() tx.Type {
	return tx.TypeAMMVote
}

// Reference: rippled AMMVote.cpp preflight
// GetFlagsMask adopts the engine FlagsMasker seam. AMMVote defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (a *AMMVote) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tfAMMVoteMask
}

func (a *AMMVote) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Reference: rippled AMMVote.cpp preflight lines 39-44
	if err := validateAssetPair(a.Asset, a.Asset2); err != nil {
		return err
	}

	if a.TradingFee > tradingFeeThreshold {
		return ter.Errorf(ter.TemBAD_FEE, "TradingFee must be 0-1000")
	}

	return nil
}

func (a *AMMVote) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

func (a *AMMVote) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// CheckExtraFeatures gates MPT pool assets on the MPTokensV2 amendment.
func (a *AMMVote) CheckExtraFeatures(rules *amendment.Rules) error {
	return requireMPTokensV2(rules, a.Asset.IsMPT() || a.Asset2.IsMPT())
}

// Preclaim requires the AMM to exist, be non-empty, and the voter to hold LP
// tokens. Reference: rippled AMMVote.cpp preclaim
func (a *AMMVote) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	amm, _, result := readAMM(view, a.Asset, a.Asset2)
	if result != ter.TesSUCCESS {
		return result
	}
	if amm.LPTokenBalance.IsZero() {
		return ter.TecAMM_EMPTY
	}
	accountID, err := state.DecodeAccountID(a.Account)
	if err != nil {
		return ter.TecAMM_INVALID_TOKENS
	}
	if ammLPHolds(view, amm, accountID).IsZero() {
		return ter.TecAMM_INVALID_TOKENS
	}
	return ter.TesSUCCESS
}

// Reference: rippled AMMVote.cpp applyVote
func (a *AMMVote) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("amm vote apply",
		"account", a.Account,
		"asset", a.Asset,
		"asset2", a.Asset2,
		"tradingFee", a.TradingFee,
	)

	accountID := ctx.AccountID
	math := newNumberMath(ctx)

	amm, ammKey, result := readAMM(ctx.View, a.Asset, a.Asset2)
	if result != ter.TesSUCCESS {
		return result
	}

	lptAMMBalance := amm.LPTokenBalance
	if lptAMMBalance.IsZero() {
		return ter.TecAMM_EMPTY
	}

	lpTokensNew := ammLPHolds(ctx.View, amm, accountID)
	if lpTokensNew.IsZero() {
		ctx.Log.Debug("amm vote: account is not LP", "account", a.Account)
		return ter.TecAMM_INVALID_TOKENS
	}

	// Check fixInnerObjTemplate: AuctionSlot must exist when amendment is enabled
	// Reference: rippled AMMVote.cpp lines 202-205
	if amm.AuctionSlot == nil && ctx.Rules().Enabled(amendment.FeatureFixInnerObjTemplate) {
		return ter.TefEXCEPTION
	}

	feeNew := a.TradingFee

	// Track minimum token holder for potential replacement
	var minTokens tx.Amount
	minTokensSet := false
	var minPos int = -1
	var minAccount [20]byte
	var minFee uint16

	updatedVoteSlots := make([]VoteSlotData, 0, voteMaxSlots)
	foundAccount := false

	scaleFactor := math.int(voteWeightScaleFactor)
	num := math.zero()
	den := math.zero()

	// Reference: rippled AMMVote.cpp:111-154 — reads actual LP balance via ammLPHolds
	for _, slot := range amm.VoteSlots {
		// Read actual LP token balance from trust line (NOT reconstructed from VoteWeight)
		// Reference: rippled AMMVote.cpp:113 — ammLPHolds(view, ammSle, votedAccount)
		lpTokens := ammLPHolds(ctx.View, amm, slot.Account)

		if lpTokens.IsZero() {
			continue
		}

		feeVal := slot.TradingFee

		if slot.Account == accountID {
			lpTokens = lpTokensNew
			feeVal = feeNew
			foundAccount = true
		}

		// Calculate new vote weight: voteWeight = lpTokens * scaleFactor / lptAMMBalance.
		// A dust LP holding less than 1/voteWeightScaleFactor of the pool gets 0.
		voteWeight := uint32(math.divToInt64(
			math.fromAmount(lpTokens).MulRounded(scaleFactor, state.RoundToNearest),
			math.fromAmount(lptAMMBalance),
		))

		// Update running totals for weighted fee: num += feeVal * lpTokens, den += lpTokens
		lpTokensNumber := math.fromAmount(lpTokens)
		num = num.AddRounded(math.int(int64(feeVal)).MulRounded(lpTokensNumber, state.RoundToNearest), state.RoundToNearest)
		den = den.AddRounded(lpTokensNumber, state.RoundToNearest)

		minComparison := 0
		if minTokensSet {
			minComparison = lpTokens.Compare(minTokens)
		}
		if !minTokensSet ||
			minComparison < 0 ||
			(minComparison == 0 && feeVal < minFee) ||
			(minComparison == 0 && feeVal == minFee && compareAccountIDs(slot.Account, minAccount) < 0) {
			minTokens = lpTokens
			minTokensSet = true
			// Index into the OUTPUT slice (where this entry will be appended),
			// matching rippled's minPos = updatedVoteSlots.size() before push_back.
			// Using the source index diverges when zero-balance voters are skipped.
			minPos = len(updatedVoteSlots)
			minAccount = slot.Account
			minFee = feeVal
		}

		updatedVoteSlots = append(updatedVoteSlots, VoteSlotData{
			Account:    slot.Account,
			TradingFee: feeVal,
			VoteWeight: voteWeight,
		})
	}

	if !foundAccount {
		lpTokensNewNumber := math.fromAmount(lpTokensNew)
		voteWeight := uint32(math.divToInt64(
			lpTokensNewNumber.MulRounded(scaleFactor, state.RoundToNearest),
			math.fromAmount(lptAMMBalance),
		))

		if len(updatedVoteSlots) < voteMaxSlots {
			updatedVoteSlots = append(updatedVoteSlots, VoteSlotData{
				Account:    accountID,
				TradingFee: feeNew,
				VoteWeight: voteWeight,
			})
			num = num.AddRounded(math.int(int64(feeNew)).MulRounded(lpTokensNewNumber, state.RoundToNearest), state.RoundToNearest)
			den = den.AddRounded(lpTokensNewNumber, state.RoundToNearest)
		} else if minTokensSet &&
			(isGreater(lpTokensNew, minTokens) || (lpTokensNew.Compare(minTokens) == 0 && feeNew > minFee)) {
			// Replace minimum token holder if new account has more tokens
			if minPos >= 0 && minPos < len(updatedVoteSlots) {
				// Remove min holder's contribution from totals
				minTokensNumber := math.fromAmount(minTokens)
				num = num.AddRounded(
					math.int(int64(minFee)).MulRounded(minTokensNumber, state.RoundToNearest).Negate(),
					state.RoundToNearest,
				)
				den = den.AddRounded(minTokensNumber.Negate(), state.RoundToNearest)

				updatedVoteSlots[minPos] = VoteSlotData{
					Account:    accountID,
					TradingFee: feeNew,
					VoteWeight: voteWeight,
				}

				num = num.AddRounded(math.int(int64(feeNew)).MulRounded(lpTokensNewNumber, state.RoundToNearest), state.RoundToNearest)
				den = den.AddRounded(lpTokensNewNumber, state.RoundToNearest)
			}
		}
	}

	// Calculate weighted average trading fee: fee = num / den
	// Reference: rippled AMMVote.cpp:209 — static_cast<int64_t>(num / den)
	var newTradingFee uint16 = 0
	if !den.IsZero() {
		newTradingFee = uint16(math.divToInt64(num, den))
	}

	amm.VoteSlots = updatedVoteSlots
	amm.TradingFee = newTradingFee

	if amm.AuctionSlot != nil {
		amm.AuctionSlot.DiscountedFee = newTradingFee / auctionSlotDiscountedFeeFraction
	}

	ammBytes, err := serializeAMMData(amm)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(ammKey, ammBytes); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}
