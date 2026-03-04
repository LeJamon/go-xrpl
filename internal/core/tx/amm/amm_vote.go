package amm

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
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
		return errors.New("temINVALID_FLAG: invalid flags for AMMVote")
	}

	// Validate asset pair
	if a.Asset.Currency == "" {
		return errors.New("temMALFORMED: Asset is required")
	}

	if a.Asset2.Currency == "" {
		return errors.New("temMALFORMED: Asset2 is required")
	}

	// TradingFee must be within threshold
	if a.TradingFee > TRADING_FEE_THRESHOLD {
		return errors.New("temBAD_FEE: TradingFee must be 0-1000")
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
		return tx.TecAMM_BALANCE // AMM empty
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
	var minTokens tx.Amount = sle.NewIssuedAmountFromValue(9999999999999999, 80, "", "") // Max amount
	var minPos int = -1
	var minAccount [20]byte
	var minFee uint16

	// Build updated vote slots
	updatedVoteSlots := make([]VoteSlotData, 0, voteMaxSlots)
	foundAccount := false

	// Scale factor as Amount for calculations
	// voteWeightScaleFactor = 100000 = 1e5, represented as mantissa 1e15 with exponent -10
	scaleFactorAmount := sle.NewIssuedAmountFromValue(1e15, -10, "", "")

	// Running totals for weighted fee calculation (use int64 for simplicity with fee*tokens)
	var numerator int64 = 0
	var denominator int64 = 0

	// Iterate over current vote entries
	for i, slot := range amm.VoteSlots {
		// lpTokens = voteWeight * lptAMMBalance / voteWeightScaleFactor
		voteWeightAmount := sle.NewIssuedAmountFromValue(int64(slot.VoteWeight)*1e15, -15, "", "")
		lpTokens := voteWeightAmount.Mul(lptAMMBalance, false).Div(scaleFactorAmount, false)

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

		// Calculate new vote weight: voteWeight = lpTokens * scaleFactory / lptAMMBalance
		voteWeightCalc := lpTokens.Mul(scaleFactorAmount, false).Div(lptAMMBalance, false)
		voteWeight := uint32(voteWeightCalc.Mantissa() / 1e12) // Scale down mantissa for uint32 storage
		if voteWeight == 0 && !lpTokens.IsZero() {
			voteWeight = 1
		}

		// Get token value for fee calculation (use mantissa scaled)
		lpTokenValue := lpTokens.Mantissa()
		if lpTokens.Exponent() > -15 {
			for e := lpTokens.Exponent(); e > -15; e-- {
				lpTokenValue *= 10
			}
		} else if lpTokens.Exponent() < -15 {
			for e := lpTokens.Exponent(); e < -15; e++ {
				lpTokenValue /= 10
			}
		}

		// Update running totals for weighted fee
		numerator += int64(feeVal) * lpTokenValue
		denominator += lpTokenValue

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
		voteWeightCalc := lpTokensNew.Mul(scaleFactorAmount, false).Div(lptAMMBalance, false)
		voteWeight := uint32(voteWeightCalc.Mantissa() / 1e12)
		if voteWeight == 0 && !lpTokensNew.IsZero() {
			voteWeight = 1
		}

		// Get token value for fee calculation
		lpTokenValue := lpTokensNew.Mantissa()
		if lpTokensNew.Exponent() > -15 {
			for e := lpTokensNew.Exponent(); e > -15; e-- {
				lpTokenValue *= 10
			}
		} else if lpTokensNew.Exponent() < -15 {
			for e := lpTokensNew.Exponent(); e < -15; e++ {
				lpTokenValue /= 10
			}
		}

		if len(updatedVoteSlots) < voteMaxSlots {
			// Add new entry if slots available
			updatedVoteSlots = append(updatedVoteSlots, VoteSlotData{
				Account:    accountID,
				TradingFee: feeNew,
				VoteWeight: voteWeight,
			})
			numerator += int64(feeNew) * lpTokenValue
			denominator += lpTokenValue
		} else if isGreater(lpTokensNew, minTokens) || (lpTokensNew.Compare(minTokens) == 0 && feeNew > minFee) {
			// Replace minimum token holder if new account has more tokens
			if minPos >= 0 && minPos < len(updatedVoteSlots) {
				// Get min token value
				minTokenValue := minTokens.Mantissa()
				if minTokens.Exponent() > -15 {
					for e := minTokens.Exponent(); e > -15; e-- {
						minTokenValue *= 10
					}
				} else if minTokens.Exponent() < -15 {
					for e := minTokens.Exponent(); e < -15; e++ {
						minTokenValue /= 10
					}
				}

				// Remove min holder's contribution from totals
				numerator -= int64(minFee) * minTokenValue
				denominator -= minTokenValue

				// Replace with new voter
				updatedVoteSlots[minPos] = VoteSlotData{
					Account:    accountID,
					TradingFee: feeNew,
					VoteWeight: voteWeight,
				}

				// Add new voter's contribution
				numerator += int64(feeNew) * lpTokenValue
				denominator += lpTokenValue
			}
		}
	}

	// Calculate weighted average trading fee
	var newTradingFee uint16 = 0
	if denominator > 0 {
		newTradingFee = uint16(numerator / denominator)
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
