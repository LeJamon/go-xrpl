package amm

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeAMMBid, func() tx.Transaction {
		return &AMMBid{BaseTx: *tx.NewBaseTx(tx.TypeAMMBid, "")}
	})
}

// AMMBid places a bid on an AMM auction slot.
type AMMBid struct {
	tx.BaseTx

	// Asset identifies the first asset of the AMM (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Asset2 identifies the second asset of the AMM (required)
	Asset2 tx.Asset `json:"Asset2" xrpl:"Asset2,asset"`

	// BidMin is the minimum bid amount (optional)
	BidMin *tx.Amount `json:"BidMin,omitempty" xrpl:"BidMin,omitempty,amount"`

	// BidMax is the maximum bid amount (optional)
	BidMax *tx.Amount `json:"BidMax,omitempty" xrpl:"BidMax,omitempty,amount"`

	// AuthAccounts are accounts to authorize for discounted trading (optional)
	AuthAccounts []AuthAccount `json:"AuthAccounts,omitempty" xrpl:"AuthAccounts,omitempty"`
}

// NewAMMBid creates a new AMMBid transaction
func NewAMMBid(account string, asset, asset2 tx.Asset) *AMMBid {
	return &AMMBid{
		BaseTx: *tx.NewBaseTx(tx.TypeAMMBid, account),
		Asset:  asset,
		Asset2: asset2,
	}
}

// TxType returns the transaction type
func (a *AMMBid) TxType() tx.Type {
	return tx.TypeAMMBid
}

// Validate validates the AMMBid transaction
// Reference: rippled AMMBid.cpp preflight
func (a *AMMBid) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags - no flags are valid for AMMBid
	if a.GetFlags()&tfAMMBidMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for AMMBid")
	}

	// Validate asset pair
	if a.Asset.Currency == "" {
		return errors.New("temMALFORMED: Asset is required")
	}

	if a.Asset2.Currency == "" {
		return errors.New("temMALFORMED: Asset2 is required")
	}

	// Validate BidMin if present
	if a.BidMin != nil {
		if err := validateAMMAmount(*a.BidMin); err != nil {
			return errors.New("temBAD_AMOUNT: invalid min slot price")
		}
	}

	// Validate BidMax if present
	if a.BidMax != nil {
		if err := validateAMMAmount(*a.BidMax); err != nil {
			return errors.New("temBAD_AMOUNT: invalid max slot price")
		}
	}

	// Max 4 auth accounts
	if len(a.AuthAccounts) > AUCTION_SLOT_MAX_AUTH_ACCOUNTS {
		return errors.New("temMALFORMED: cannot have more than 4 AuthAccounts")
	}

	// Check for duplicate auth accounts and self-authorization
	if len(a.AuthAccounts) > 0 {
		seen := make(map[string]bool)
		for _, authAcct := range a.AuthAccounts {
			acct := authAcct.AuthAccount.Account
			// Cannot authorize self
			if acct == a.Common.Account {
				return errors.New("temMALFORMED: cannot authorize self in AuthAccounts")
			}
			// Check for duplicates
			if seen[acct] {
				return errors.New("temMALFORMED: duplicate account in AuthAccounts")
			}
			seen[acct] = true
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AMMBid) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// RequiredAmendments returns the amendments required for this transaction type
func (a *AMMBid) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Apply applies the AMMBid transaction to ledger state.
// Reference: rippled AMMBid.cpp applyBid
func (a *AMMBid) Apply(ctx *tx.ApplyContext) tx.Result {
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

	// Get bidder's LP token balance from trustline
	// Reference: rippled AMMBid.cpp preclaim line 129
	lpTokens := ammLPHolds(ctx.View, amm, accountID)
	if lpTokens.IsZero() {
		// Account is not a liquidity provider
		return tx.TecAMM_INVALID_TOKENS
	}

	// Get LP token issue for validation
	// Reference: rippled AMMBid.cpp preclaim lines 137-160
	lptCurrency := generateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
	ammAccountAddr, _ := encodeAccountID(amm.Account)

	// Get bid amounts from transaction
	bidMin := zeroAmount(tx.Asset{})
	bidMax := zeroAmount(tx.Asset{})

	if a.BidMin != nil {
		bidMin = *a.BidMin
		// Validate that BidMin is LP tokens (not regular IOU)
		if bidMin.Currency != lptCurrency || bidMin.Issuer != ammAccountAddr {
			return tx.TemBAD_AMM_TOKENS
		}
		if isGreater(bidMin, lpTokens) || isGreater(bidMin, lptAMMBalance) {
			return tx.TecAMM_INVALID_TOKENS
		}
	}
	if a.BidMax != nil {
		bidMax = *a.BidMax
		// Validate that BidMax is LP tokens (not regular IOU)
		if bidMax.Currency != lptCurrency || bidMax.Issuer != ammAccountAddr {
			return tx.TemBAD_AMM_TOKENS
		}
		if isGreater(bidMax, lpTokens) || isGreater(bidMax, lptAMMBalance) {
			return tx.TecAMM_INVALID_TOKENS
		}
	}
	if !bidMin.IsZero() && !bidMax.IsZero() && isGreater(bidMin, bidMax) {
		return tx.TecAMM_INVALID_TOKENS
	}

	// Calculate trading fee as an Amount fraction
	tradingFee := getFee(amm.TradingFee)

	// Minimum slot price = lptAMMBalance * tradingFee / 25
	// minSlotPrice = lptAMMBalance * tradingFee / auctionSlotMinFeeFraction
	minSlotPriceFrac := tradingFee.Div(sle.NewIssuedAmountFromValue(int64(auctionSlotMinFeeFraction)*1e15, -15, "", ""), false)
	minSlotPrice := lptAMMBalance.Mul(minSlotPriceFrac, false)

	// Calculate discounted fee
	_ = amm.TradingFee / uint16(auctionSlotDiscountedFee) // discountedFee for future use

	// Get current time (simplified - would use ledger close time)
	currentTime := uint32(0) // Would be ctx.View.info().parentCloseTime

	// Initialize auction slot if needed
	if amm.AuctionSlot == nil {
		amm.AuctionSlot = &AuctionSlotData{
			AuthAccounts: make([][20]byte, 0),
			Price:        zeroAmount(tx.Asset{}),
		}
	}

	// Calculate time slot (0-19)
	var timeSlot *int
	if amm.AuctionSlot.Expiration > 0 && currentTime < amm.AuctionSlot.Expiration {
		elapsed := amm.AuctionSlot.Expiration - auctionSlotTotalTimeSecs
		if currentTime >= elapsed {
			slot := int((currentTime - elapsed) / auctionSlotIntervalDuration)
			if slot >= 0 && slot < auctionSlotTimeIntervals {
				timeSlot = &slot
			}
		}
	}

	// Check if current owner is valid
	validOwner := false
	if timeSlot != nil && *timeSlot < auctionSlotTimeIntervals-1 {
		// Check if owner account exists
		var zeroAccount [20]byte
		if amm.AuctionSlot.Account != zeroAccount {
			ownerKey := keylet.Account(amm.AuctionSlot.Account)
			exists, _ := ctx.View.Exists(ownerKey)
			validOwner = exists
		}
	}

	// Calculate pay price based on slot state
	var computedPrice tx.Amount
	var fractionRemaining tx.Amount
	pricePurchased := amm.AuctionSlot.Price

	if !validOwner || timeSlot == nil {
		// Slot is unowned or expired - pay minimum price
		computedPrice = minSlotPrice
		fractionRemaining = zeroAmount(tx.Asset{})
	} else {
		// Slot is owned - calculate price based on time interval
		// fractionUsed = (timeSlot + 1) / auctionSlotTimeIntervals
		slotNum := *timeSlot + 1
		fractionUsed := sle.NewIssuedAmountFromValue(int64(slotNum)*1e15, -15, "", "").Div(
			sle.NewIssuedAmountFromValue(int64(auctionSlotTimeIntervals)*1e15, -15, "", ""), false)
		fractionRemaining, _ = oneAmount().Sub(fractionUsed)

		// price1p05 = pricePurchased * 1.05
		multiplier := sle.NewIssuedAmountFromValue(105*1e13, -15, "", "") // 1.05
		price1p05 := pricePurchased.Mul(multiplier, false)

		if *timeSlot == 0 {
			// First interval: price = pricePurchased * 1.05 + minSlotPrice
			computedPrice, _ = price1p05.Add(minSlotPrice)
		} else {
			// Other intervals: price = pricePurchased * 1.05 * (1 - fractionUsed^60) + minSlotPrice
			// For simplicity, approximate with linear decay: price = pricePurchased * 1.05 * fractionRemaining + minSlotPrice
			// This is a simplification - a full implementation would use proper power function
			decayFactor := fractionRemaining
			decayedPrice := price1p05.Mul(decayFactor, false)
			computedPrice, _ = decayedPrice.Add(minSlotPrice)
		}
	}

	// Determine actual pay price based on bidMin/bidMax
	var payPrice tx.Amount
	hasBidMin := !bidMin.IsZero()
	hasBidMax := !bidMax.IsZero()

	if hasBidMin && hasBidMax {
		// Both min/max specified
		if isLessOrEqual(computedPrice, bidMax) {
			payPrice = maxAmount(computedPrice, bidMin)
		} else {
			return tx.TecAMM_FAILED
		}
	} else if hasBidMin {
		// Only min specified
		payPrice = maxAmount(computedPrice, bidMin)
	} else if hasBidMax {
		// Only max specified
		if isLessOrEqual(computedPrice, bidMax) {
			payPrice = computedPrice
		} else {
			return tx.TecAMM_FAILED
		}
	} else {
		// Neither specified - pay computed price
		payPrice = computedPrice
	}

	// Check bidder has enough tokens
	if isGreater(payPrice, lpTokens) {
		return tx.TecAMM_INVALID_TOKENS
	}

	// Calculate refund and burn amounts
	var refund tx.Amount = zeroAmount(tx.Asset{})
	var burn tx.Amount = payPrice

	if validOwner && timeSlot != nil {
		// Refund previous owner: refund = fractionRemaining * pricePurchased
		refund = fractionRemaining.Mul(pricePurchased, false)
		if isGreater(refund, payPrice) {
			return tx.TefINTERNAL // Should not happen
		}
		burn, _ = payPrice.Sub(refund)

		// Transfer refund to previous owner
		// In full implementation, would use accountSend
		_ = refund
	}

	// Burn tokens (reduce LP balance)
	if isGreater(burn, lptAMMBalance) {
		return tx.TefINTERNAL
	}
	newLPBalance, _ := amm.LPTokenBalance.Sub(burn)
	amm.LPTokenBalance = newLPBalance

	// Update auction slot
	amm.AuctionSlot.Account = accountID
	amm.AuctionSlot.Expiration = currentTime + auctionSlotTotalTimeSecs
	amm.AuctionSlot.Price = payPrice

	// Parse auth accounts if provided
	if a.AuthAccounts != nil {
		amm.AuctionSlot.AuthAccounts = make([][20]byte, 0, len(a.AuthAccounts))
		for _, authAccountEntry := range a.AuthAccounts {
			authAccountID, err := sle.DecodeAccountID(authAccountEntry.AuthAccount.Account)
			if err == nil {
				amm.AuctionSlot.AuthAccounts = append(amm.AuctionSlot.AuthAccounts, authAccountID)
			}
		}
	} else {
		amm.AuctionSlot.AuthAccounts = make([][20]byte, 0)
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
