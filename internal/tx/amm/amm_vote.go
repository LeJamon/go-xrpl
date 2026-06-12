package amm

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
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
func (a *AMMVote) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags - no flags are valid for AMMVote
	if a.GetFlags()&tfAMMVoteMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for AMMVote")
	}

	// Validate asset pair
	// Reference: rippled AMMVote.cpp preflight lines 39-44
	if err := validateAssetPair(a.Asset, a.Asset2); err != nil {
		return err
	}

	// TradingFee must be within threshold
	if a.TradingFee > tradingFeeThreshold {
		return tx.Errorf(tx.TemBAD_FEE, "TradingFee must be 0-1000")
	}

	return nil
}

func (a *AMMVote) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

func (a *AMMVote) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Preclaim requires the AMM to exist, be non-empty, and the voter to hold LP
// tokens. Reference: rippled AMMVote.cpp preclaim
func (a *AMMVote) Preclaim(view tx.LedgerView, _ tx.EngineConfig) tx.Result {
	amm, _, result := readAMM(view, a.Asset, a.Asset2)
	if result != tx.TesSUCCESS {
		return result
	}
	if amm.LPTokenBalance.IsZero() {
		return tx.TecAMM_EMPTY
	}
	accountID, err := state.DecodeAccountID(a.Account)
	if err != nil {
		return tx.TecAMM_INVALID_TOKENS
	}
	if ammLPHolds(view, amm, accountID).IsZero() {
		return tx.TecAMM_INVALID_TOKENS
	}
	return tx.TesSUCCESS
}

// Reference: rippled AMMVote.cpp applyVote
func (a *AMMVote) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("amm vote apply",
		"account", a.Account,
		"asset", a.Asset,
		"asset2", a.Asset2,
		"tradingFee", a.TradingFee,
	)

	accountID := ctx.AccountID

	amm, ammKey, result := readAMM(ctx.View, a.Asset, a.Asset2)
	if result != tx.TesSUCCESS {
		return result
	}

	lptAMMBalance := amm.LPTokenBalance
	if lptAMMBalance.IsZero() {
		return tx.TecAMM_EMPTY
	}

	// Get voter's LP token balance from trustline
	lpTokensNew := ammLPHolds(ctx.View, amm, accountID)
	if lpTokensNew.IsZero() {
		ctx.Log.Debug("amm vote: account is not LP", "account", a.Account)
		return tx.TecAMM_INVALID_TOKENS
	}

	// Check fixInnerObjTemplate: AuctionSlot must exist when amendment is enabled
	// Reference: rippled AMMVote.cpp lines 202-205
	if amm.AuctionSlot == nil && ctx.Rules().Enabled(amendment.FeatureFixInnerObjTemplate) {
		return tx.TefEXCEPTION
	}

	feeNew := a.TradingFee

	// Track minimum token holder for potential replacement
	var minTokens tx.Amount = state.NewIssuedAmountFromValue(9999999999999999, 80, "", "") // Max amount
	var minPos int = -1
	var minAccount [20]byte
	var minFee uint16

	// Build updated vote slots
	updatedVoteSlots := make([]VoteSlotData, 0, voteMaxSlots)
	foundAccount := false

	// Scale factor as Amount for calculations
	// voteWeightScaleFactor = 100000 = 1e5, represented as mantissa 1e15 with exponent -10
	scaleFactorAmount := state.NewIssuedAmountFromValue(1e15, -10, "", "")

	// Running totals for weighted fee calculation.
	// Use tx.Amount (IOU-style) to avoid int64 overflow on feeVal * lpTokens.
	// Reference: rippled uses Number (arbitrary precision) for num/den.
	var num tx.Amount = state.NewIssuedAmountFromFloat64(0, "", "")
	var den tx.Amount = state.NewIssuedAmountFromFloat64(0, "", "")

	// Iterate over current vote entries
	// Reference: rippled AMMVote.cpp:111-154 — reads actual LP balance via ammLPHolds
	for _, slot := range amm.VoteSlots {
		// Read actual LP token balance from trust line (NOT reconstructed from VoteWeight)
		// Reference: rippled AMMVote.cpp:113 — ammLPHolds(view, ammSle, votedAccount)
		lpTokens := ammLPHolds(ctx.View, amm, slot.Account)

		if lpTokens.IsZero() {
			// Skip entries with no tokens
			continue
		}

		feeVal := slot.TradingFee

		// Check if this is the voting account
		if slot.Account == accountID {
			lpTokens = lpTokensNew
			feeVal = feeNew
			foundAccount = true
		}

		// Calculate new vote weight: voteWeight = lpTokens * scaleFactor / lptAMMBalance.
		// A dust LP holding less than 1/voteWeightScaleFactor of the pool gets 0.
		voteWeight := uint32(numberDivToInt64(lpTokens.Mul(scaleFactorAmount, false), lptAMMBalance))

		// Update running totals for weighted fee: num += feeVal * lpTokens, den += lpTokens
		feeAmount := state.NewIssuedAmountFromFloat64(float64(feeVal), "", "")
		num, _ = num.Add(feeAmount.Mul(lpTokens, false))
		den, _ = den.Add(lpTokens)

		// Track minimum for potential replacement
		if lpTokens.Compare(minTokens) < 0 ||
			(lpTokens.Compare(minTokens) == 0 && feeVal < minFee) ||
			(lpTokens.Compare(minTokens) == 0 && feeVal == minFee && compareAccountIDs(slot.Account, minAccount) < 0) {
			minTokens = lpTokens
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

	// If account doesn't have a vote entry yet
	if !foundAccount {
		voteWeight := uint32(numberDivToInt64(lpTokensNew.Mul(scaleFactorAmount, false), lptAMMBalance))

		if len(updatedVoteSlots) < voteMaxSlots {
			// Add new entry if slots available
			updatedVoteSlots = append(updatedVoteSlots, VoteSlotData{
				Account:    accountID,
				TradingFee: feeNew,
				VoteWeight: voteWeight,
			})
			feeAmount := state.NewIssuedAmountFromFloat64(float64(feeNew), "", "")
			num, _ = num.Add(feeAmount.Mul(lpTokensNew, false))
			den, _ = den.Add(lpTokensNew)
		} else if isGreater(lpTokensNew, minTokens) || (lpTokensNew.Compare(minTokens) == 0 && feeNew > minFee) {
			// Replace minimum token holder if new account has more tokens
			if minPos >= 0 && minPos < len(updatedVoteSlots) {
				// Remove min holder's contribution from totals
				minFeeAmt := state.NewIssuedAmountFromFloat64(float64(minFee), "", "")
				num, _ = num.Sub(minFeeAmt.Mul(minTokens, false))
				den, _ = den.Sub(minTokens)

				// Replace with new voter
				updatedVoteSlots[minPos] = VoteSlotData{
					Account:    accountID,
					TradingFee: feeNew,
					VoteWeight: voteWeight,
				}

				// Add new voter's contribution
				feeAmount := state.NewIssuedAmountFromFloat64(float64(feeNew), "", "")
				num, _ = num.Add(feeAmount.Mul(lpTokensNew, false))
				den, _ = den.Add(lpTokensNew)
			}
		}
	}

	// Calculate weighted average trading fee: fee = num / den
	// Reference: rippled AMMVote.cpp:209 — static_cast<int64_t>(num / den)
	var newTradingFee uint16 = 0
	if !den.IsZero() {
		newTradingFee = uint16(numberDivToInt64(num, den))
	}

	// Update AMM data
	amm.VoteSlots = updatedVoteSlots
	amm.TradingFee = newTradingFee

	// Update discounted fee in auction slot
	if amm.AuctionSlot != nil {
		amm.AuctionSlot.DiscountedFee = newTradingFee / auctionSlotDiscountedFeeFraction
	}

	// Persist updated AMM
	ammBytes, err := serializeAMMData(amm)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(ammKey, ammBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}
