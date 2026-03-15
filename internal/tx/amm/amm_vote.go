package amm

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

func init() {
	tx.Register(tx.TypeAMMVote, func() tx.Transaction {
		return &AMMVote{BaseTx: *tx.NewBaseTx(tx.TypeAMMVote, "")}
	})
}

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

// TxType returns the transaction type
func (a *AMMVote) TxType() tx.Type {
	return tx.TypeAMMVote
}

// Validate validates the AMMVote transaction
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
	if a.Asset.Currency == "" {
		return tx.Errorf(tx.TemMALFORMED, "Asset is required")
	}

	if a.Asset2.Currency == "" {
		return tx.Errorf(tx.TemMALFORMED, "Asset2 is required")
	}

	// TradingFee must be within threshold
	if a.TradingFee > TRADING_FEE_THRESHOLD {
		return tx.Errorf(tx.TemBAD_FEE, "TradingFee must be 0-1000")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AMMVote) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// RequiredAmendments returns the amendments required for this transaction type
func (a *AMMVote) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Apply applies the AMMVote transaction to ledger state.
// Reference: rippled AMMVote.cpp applyVote
func (a *AMMVote) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("amm vote apply",
		"account", a.Account,
		"asset", a.Asset,
		"asset2", a.Asset2,
		"tradingFee", a.TradingFee,
	)

	accountID := ctx.AccountID

	// Find the AMM
	ammKey := computeAMMKeylet(a.Asset, a.Asset2)

	ammRawData, err := ctx.View.Read(ammKey)
	if err != nil || ammRawData == nil {
		return TerNO_AMM
	}

	// Parse AMM data
	amm, err := parseAMMData(ammRawData)
	if err != nil {
		return tx.TefINTERNAL
	}

	lptAMMBalance := amm.LPTokenBalance
	if lptAMMBalance.IsZero() {
		return tx.TecAMM_EMPTY
	}

	// Get voter's LP token balance from trustline
	// Reference: rippled AMMVote.cpp preclaim line 73-79
	lpTokensNew := ammLPHolds(ctx.View, amm, accountID)
	if lpTokensNew.IsZero() {
		// Account is not a liquidity provider
		return tx.TecAMM_INVALID_TOKENS
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
	for i, slot := range amm.VoteSlots {
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

		// Calculate new vote weight: voteWeight = lpTokens * scaleFactor / lptAMMBalance
		// Reference: rippled AMMVote.cpp:137-139
		voteWeightCalc := numberDiv(lpTokens.Mul(scaleFactorAmount, false), lptAMMBalance)
		voteWeight := uint32(voteWeightCalc.Float64())
		if voteWeight == 0 && !lpTokens.IsZero() {
			voteWeight = 1
		}

		// Update running totals for weighted fee: num += feeVal * lpTokens, den += lpTokens
		feeAmount := state.NewIssuedAmountFromFloat64(float64(feeVal), "", "")
		num, _ = num.Add(feeAmount.Mul(lpTokens, false))
		den, _ = den.Add(lpTokens)

		// Track minimum for potential replacement
		if lpTokens.Compare(minTokens) < 0 ||
			(lpTokens.Compare(minTokens) == 0 && feeVal < minFee) ||
			(lpTokens.Compare(minTokens) == 0 && feeVal == minFee && compareAccountIDs(slot.Account, minAccount) < 0) {
			minTokens = lpTokens
			minPos = i
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
		voteWeightCalc := numberDiv(lpTokensNew.Mul(scaleFactorAmount, false), lptAMMBalance)
		voteWeight := uint32(voteWeightCalc.Float64())
		if voteWeight == 0 && !lpTokensNew.IsZero() {
			voteWeight = 1
		}

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
	var newTradingFee uint16 = 0
	if !den.IsZero() {
		feeResult := numberDiv(num, den)
		newTradingFee = uint16(feeResult.Float64())
	}

	// Update AMM data
	amm.VoteSlots = updatedVoteSlots
	amm.TradingFee = newTradingFee

	// Update discounted fee in auction slot
	if amm.AuctionSlot != nil {
		discountedFee := newTradingFee / auctionSlotDiscountedFee
		_ = discountedFee
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
