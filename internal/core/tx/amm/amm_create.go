package amm

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeAMMCreate, func() tx.Transaction {
		return &AMMCreate{BaseTx: *tx.NewBaseTx(tx.TypeAMMCreate, "")}
	})
}

// AMMCreate creates an Automated Market Maker (AMM) instance.
type AMMCreate struct {
	tx.BaseTx

	// Amount is the first asset to deposit (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Amount2 is the second asset to deposit (required)
	Amount2 tx.Amount `json:"Amount2" xrpl:"Amount2,amount"`

	// TradingFee is the fee in basis points (0-1000, where 1000 = 1%)
	TradingFee uint16 `json:"TradingFee" xrpl:"TradingFee"`
}

// NewAMMCreate creates a new AMMCreate transaction
func NewAMMCreate(account string, amount1, amount2 tx.Amount, tradingFee uint16) *AMMCreate {
	return &AMMCreate{
		BaseTx:     *tx.NewBaseTx(tx.TypeAMMCreate, account),
		Amount:     amount1,
		Amount2:    amount2,
		TradingFee: tradingFee,
	}
}

// TxType returns the transaction type
func (a *AMMCreate) TxType() tx.Type {
	return tx.TypeAMMCreate
}

// Validate validates the AMMCreate transaction
// Reference: rippled AMMCreate.cpp preflight
func (a *AMMCreate) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags - no flags are valid for AMMCreate
	if a.GetFlags()&tfAMMCreateMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for AMMCreate")
	}

	// Assets cannot be the same (same currency and issuer)
	// Reference: rippled AMMCreate.cpp line 52-57
	if a.Amount.Currency == a.Amount2.Currency && a.Amount.Issuer == a.Amount2.Issuer {
		return errors.New("temBAD_AMM_TOKENS: tokens can not have the same currency/issuer")
	}

	// Validate amounts using invalidAMMAmount logic
	// Reference: rippled AMMCreate.cpp line 59-68
	if err := validateAMMAmount(a.Amount); err != nil {
		return errors.New("temBAD_AMOUNT: invalid asset1 amount")
	}
	if err := validateAMMAmount(a.Amount2); err != nil {
		return errors.New("temBAD_AMOUNT: invalid asset2 amount")
	}

	// TradingFee must be 0-1000 (0-1%)
	// Reference: rippled AMMCreate.cpp line 71-75
	if a.TradingFee > TRADING_FEE_THRESHOLD {
		return errors.New("temBAD_FEE: TradingFee must be 0-1000")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AMMCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// RequiredAmendments returns the amendments required for this transaction type
func (a *AMMCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Apply applies the AMMCreate transaction to ledger state.
// Reference: rippled AMMCreate.cpp preclaim + doApply/applyCreate
func (a *AMMCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	// Build assets for keylet computation
	asset1 := tx.Asset{Currency: a.Amount.Currency, Issuer: a.Amount.Issuer}
	asset2 := tx.Asset{Currency: a.Amount2.Currency, Issuer: a.Amount2.Issuer}

	// =========================================================================
	// PRECLAIM CHECKS - Reference: rippled AMMCreate.cpp preclaim lines 88-215
	// =========================================================================

	// Check if AMM already exists for the token pair
	// Reference: rippled AMMCreate.cpp line 95-100
	ammKey := computeAMMKeylet(asset1, asset2)
	exists, _ := ctx.View.Exists(ammKey)
	if exists {
		return tx.TecDUPLICATE
	}

	// Check authorization for both assets
	// Reference: rippled AMMCreate.cpp line 102-116
	if result := requireAuth(ctx.View, asset1, accountID); result != tx.TesSUCCESS {
		return result
	}
	if result := requireAuth(ctx.View, asset2, accountID); result != tx.TesSUCCESS {
		return result
	}

	// Check if either asset is frozen (globally or individually)
	// Reference: rippled AMMCreate.cpp line 119-124
	if isFrozen(ctx.View, accountID, asset1) || isFrozen(ctx.View, accountID, asset2) {
		return tx.TecFROZEN
	}

	// Check DefaultRipple is set on issuers
	// Reference: rippled AMMCreate.cpp line 126-142
	if noDefaultRipple(ctx.View, asset1) || noDefaultRipple(ctx.View, asset2) {
		return tx.TerNO_RIPPLE
	}

	// Check reserve for LP token trustline
	// Reference: rippled AMMCreate.cpp line 145-151
	// The account needs enough XRP for the reserve after creating the LP trustline
	xrpLiquid := xrpLiquidBalance(ctx.View, accountID, 1)
	if xrpLiquid <= 0 {
		return TecINSUF_RESERVE_LINE
	}

	// Check sufficient balance for both assets
	// Reference: rippled AMMCreate.cpp line 153-170
	if insufficientBalance(ctx.View, accountID, a.Amount, xrpLiquid) ||
		insufficientBalance(ctx.View, accountID, a.Amount2, xrpLiquid) {
		return TecUNFUNDED_AMM
	}

	// Check that neither amount is an LP token (can't create AMM with LP tokens)
	// Reference: rippled AMMCreate.cpp line 172-184
	if isLPToken(ctx.View, a.Amount) || isLPToken(ctx.View, a.Amount2) {
		return tx.TecAMM_INVALID_TOKENS
	}

	// Check clawback - if featureAMMClawback is not enabled, reject clawback-enabled issuers
	// Reference: rippled AMMCreate.cpp line 194-214
	// Note: We assume AMMClawback amendment is enabled in this implementation
	// If not enabled, we would need to check:
	// if result := clawbackDisabled(ctx.View, asset1); result != tx.TesSUCCESS { return result }
	// if result := clawbackDisabled(ctx.View, asset2); result != tx.TesSUCCESS { return result }

	// =========================================================================
	// APPLY - Reference: rippled AMMCreate.cpp applyCreate lines 217-356
	// =========================================================================

	// Compute the AMM account ID from keylet
	ammAccountID := computeAMMAccountID(ammKey.Key)
	ammAccountAddr, _ := encodeAccountID(ammAccountID)

	// Check if AMM account already exists (should not happen)
	// Reference: rippled AMMCreate.cpp line 230-236
	ammAccountKey := keylet.Account(ammAccountID)
	acctExists, _ := ctx.View.Exists(ammAccountKey)
	if acctExists {
		return tx.TecDUPLICATE
	}

	// Sort assets by canonical ordering (minmax)
	// Reference: rippled AMMCreate.cpp line 262-264
	sortedAsset1, sortedAsset2, sortedAmount1, sortedAmount2 := sortAssets(asset1, asset2, a.Amount, a.Amount2)

	// Generate LP token currency code using sorted assets
	lptCurrency := generateAMMLPTCurrency(sortedAsset1.Currency, sortedAsset2.Currency)

	// Check LP token trustline doesn't already exist
	// Reference: rippled AMMCreate.cpp line 241-247
	lptIssuerID := ammAccountID
	lptKey := keylet.Line(accountID, lptIssuerID, lptCurrency)
	lptExists, _ := ctx.View.Exists(lptKey)
	if lptExists {
		return tx.TecDUPLICATE
	}

	// Calculate initial LP token balance: sqrt(amount1 * amount2)
	// Reference: rippled AMMCreate.cpp line 256
	lpTokenBalance := calculateLPTokens(sortedAmount1, sortedAmount2)
	if lpTokenBalance.IsZero() {
		return tx.TecAMM_BALANCE
	}

	// Create the AMM pseudo-account with lsfAMM flag
	// Reference: rippled AMMCreate.cpp line 230 (createPseudoAccount)
	ammAccount := &sle.AccountRoot{
		Account:    ammAccountAddr,
		Balance:    0,
		Sequence:   0,
		OwnerCount: 1, // For the AMM entry itself
		Flags:      sle.LsfAMM,
	}

	// Create the AMM entry with sorted assets
	// Reference: rippled AMMCreate.cpp line 259-267
	// IMPORTANT: Asset balances are NOT stored in the AMM entry.
	// They are stored in:
	// - XRP: AMM account's AccountRoot.Balance
	// - IOU: Trustlines between AMM account and asset issuers
	ammData := &AMMData{
		Account:        ammAccountID,
		TradingFee:     a.TradingFee,
		LPTokenBalance: lpTokenBalance,
		Asset:          sortedAsset1,
		Asset2:         sortedAsset2,
		OwnerNode:      0, // Will be set when inserting into owner directory
		VoteSlots:      make([]VoteSlotData, 0),
	}

	// Initialize creator's vote slot
	// Reference: rippled AMMUtils.cpp initializeFeeAuctionVote line 349-356
	// Creator gets VOTE_WEIGHT_SCALE_FACTOR (100000) as their vote weight
	creatorVote := VoteSlotData{
		Account:    accountID,
		TradingFee: a.TradingFee,
		VoteWeight: uint32(VOTE_WEIGHT_SCALE_FACTOR),
	}
	ammData.VoteSlots = append(ammData.VoteSlots, creatorVote)

	// Initialize auction slot (creator gets initial slot for free)
	// Reference: rippled AMMUtils.cpp initializeFeeAuctionVote line 357-381
	// Expiration = current time + TOTAL_TIME_SLOT_SECS (24 hours)
	expiration := ctx.Config.ParentCloseTime + uint32(TOTAL_TIME_SLOT_SECS)
	discountedFee := uint16(0)
	if a.TradingFee > 0 {
		discountedFee = a.TradingFee / uint16(AUCTION_SLOT_DISCOUNTED_FEE_FRACTION)
	}
	ammData.AuctionSlot = &AuctionSlotData{
		Account:       accountID,
		Expiration:    expiration,
		Price:         zeroAmount(tx.Asset{Currency: lptCurrency, Issuer: ammAccountAddr}),
		DiscountedFee: discountedFee,
		AuthAccounts:  make([][20]byte, 0),
	}

	// Store the AMM pseudo-account
	ammAccountBytes, err := sle.SerializeAccountRoot(ammAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Insert(ammAccountKey, ammAccountBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Store the AMM entry
	// Note: ammData.Account should be the AMM pseudo-account ID (already set above)
	ammBytes, err := serializeAMMData(ammData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Insert(ammKey, ammBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Send LP tokens to creator
	// Reference: rippled AMMCreate.cpp line 278-283
	lptAsset := tx.Asset{
		Currency: lptCurrency,
		Issuer:   ammAccountAddr,
	}
	if err := createLPTokenTrustline(accountID, lptAsset, lpTokenBalance, ctx.View); err != nil {
		return TecINSUF_RESERVE_LINE
	}

	// Transfer assets from creator to AMM and set lsfAMMNode on trustlines
	// Reference: rippled AMMCreate.cpp sendAndTrustSet lines 285-309
	isXRP1 := sortedAsset1.Currency == "" || sortedAsset1.Currency == "XRP"
	isXRP2 := sortedAsset2.Currency == "" || sortedAsset2.Currency == "XRP"

	// Transfer first asset
	if isXRP1 {
		drops := uint64(sortedAmount1.Drops())
		ctx.Account.Balance -= drops
		ammAccount.Balance += drops
	} else {
		if err := createOrUpdateAMMTrustline(ammAccountID, sortedAsset1, sortedAmount1, ctx.View); err != nil {
			return TecNO_LINE
		}
		// Set lsfAMMNode flag on the AMM's trustline
		if err := setAMMNodeFlag(ammAccountID, sortedAsset1, ctx.View); err != nil {
			return tx.TefINTERNAL
		}
		// Debit from creator's trustline
		issuerID1, _ := sle.DecodeAccountID(sortedAsset1.Issuer)
		if err := updateTrustlineBalanceInView(accountID, issuerID1, sortedAsset1.Currency, sortedAmount1.Negate(), ctx.View); err != nil {
			return TecUNFUNDED_AMM
		}
	}

	// Transfer second asset
	if isXRP2 {
		drops := uint64(sortedAmount2.Drops())
		ctx.Account.Balance -= drops
		ammAccount.Balance += drops
	} else {
		if err := createOrUpdateAMMTrustline(ammAccountID, sortedAsset2, sortedAmount2, ctx.View); err != nil {
			return TecNO_LINE
		}
		// Set lsfAMMNode flag on the AMM's trustline
		if err := setAMMNodeFlag(ammAccountID, sortedAsset2, ctx.View); err != nil {
			return tx.TefINTERNAL
		}
		issuerID2, _ := sle.DecodeAccountID(sortedAsset2.Issuer)
		if err := updateTrustlineBalanceInView(accountID, issuerID2, sortedAsset2.Currency, sortedAmount2.Negate(), ctx.View); err != nil {
			return TecUNFUNDED_AMM
		}
	}

	// Update creator account (owner count increases for LP token trustline)
	ctx.Account.OwnerCount++

	// Persist updated creator account
	accountKey := keylet.Account(accountID)
	accountBytes, err := sle.SerializeAccountRoot(ctx.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(accountKey, accountBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Update AMM account balance (for XRP)
	ammAccountBytes, err = sle.SerializeAccountRoot(ammAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(ammAccountKey, ammAccountBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// requireAuth checks if the account is authorized for the asset.
// Reference: rippled View.cpp requireAuth() lines 2404-2433
func requireAuth(view tx.LedgerView, asset tx.Asset, accountID [20]byte) tx.Result {
	// XRP doesn't require authorization
	if asset.Currency == "" || asset.Currency == "XRP" {
		return tx.TesSUCCESS
	}

	issuerID, err := sle.DecodeAccountID(asset.Issuer)
	if err != nil {
		return tx.TesSUCCESS // Invalid issuer - pass (will fail elsewhere)
	}

	// If account is issuer, OK
	if accountID == issuerID {
		return tx.TesSUCCESS
	}

	// Read trustline first
	trustLineKey := keylet.Line(accountID, issuerID, asset.Currency)
	trustLineData, err := view.Read(trustLineKey)

	// Read issuer account
	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		// If issuer account doesn't exist, check passes
		// Reference: rippled line 2421-2422 - only checks if issuerAccount exists
		return tx.TesSUCCESS
	}

	issuerAccount, err := sle.ParseAccountRoot(issuerData)
	if err != nil {
		// Can't parse issuer account - pass (defensive)
		return tx.TesSUCCESS
	}

	// If issuer doesn't require auth, OK
	if (issuerAccount.Flags & sle.LsfRequireAuth) == 0 {
		return tx.TesSUCCESS
	}

	// Issuer requires auth - check trustline
	if trustLineData == nil {
		return tx.TecNO_LINE
	}

	rs, err := sle.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TecNO_AUTH
	}

	// Check authorization flags based on canonical ordering
	// Reference: rippled line 2425-2428: (account > issue.account) ? lsfLowAuth : lsfHighAuth
	// Note: In rippled, "account > issue.account" means account is HIGH side
	accountIsHigh := sle.CompareAccountIDsForLine(accountID, issuerID) > 0
	if accountIsHigh {
		if (rs.Flags & sle.LsfLowAuth) == 0 {
			return tx.TecNO_AUTH
		}
	} else {
		if (rs.Flags & sle.LsfHighAuth) == 0 {
			return tx.TecNO_AUTH
		}
	}

	return tx.TesSUCCESS
}

// isFrozen checks if the asset is frozen for the account.
// Reference: rippled AMMCreate.cpp line 119-124
func isFrozen(view tx.LedgerView, accountID [20]byte, asset tx.Asset) bool {
	// XRP cannot be frozen
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}

	// Check global freeze
	if tx.IsGlobalFrozen(view, asset.Issuer) {
		return true
	}

	// Check individual trustline freeze
	issuerID, err := sle.DecodeAccountID(asset.Issuer)
	if err != nil {
		return false
	}

	return tx.IsTrustlineFrozen(view, accountID, issuerID, asset.Currency)
}

// noDefaultRipple checks if the issuer does not have DefaultRipple set.
// Reference: rippled AMMCreate.cpp lines 126-135
// Returns true if DefaultRipple is NOT set (which is a problem for AMM)
// Returns false if:
//   - Asset is XRP
//   - Issuer account doesn't exist (check passes)
//   - DefaultRipple IS set (OK)
func noDefaultRipple(view tx.LedgerView, asset tx.Asset) bool {
	// XRP doesn't need DefaultRipple
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}

	issuerID, err := sle.DecodeAccountID(asset.Issuer)
	if err != nil {
		// Invalid issuer - return false (not a DefaultRipple problem)
		return false
	}

	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		// If issuer account doesn't exist, return false
		// Reference: rippled line 134 returns false if issuerAccount can't be read
		return false
	}

	issuerAccount, err := sle.ParseAccountRoot(issuerData)
	if err != nil {
		// Can't parse issuer account - return false (defensive)
		return false
	}

	// Return true if DefaultRipple is NOT set (problem)
	return (issuerAccount.Flags & sle.LsfDefaultRipple) == 0
}

// xrpLiquidBalance returns the XRP available after reserving for ownerCount additional objects.
// Reference: rippled AMMCreate.cpp line 145
func xrpLiquidBalance(view tx.LedgerView, accountID [20]byte, additionalOwnerCount int) int64 {
	accountKey := keylet.Account(accountID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return 0
	}

	account, err := sle.ParseAccountRoot(data)
	if err != nil {
		return 0
	}

	// Base reserve + owner reserve * (ownerCount + additional)
	// Using standard XRPL reserves: 10 XRP base + 2 XRP per owner
	baseReserve := int64(10_000_000)   // 10 XRP in drops
	ownerReserve := int64(2_000_000)   // 2 XRP in drops
	totalReserve := baseReserve + ownerReserve*int64(account.OwnerCount+uint32(additionalOwnerCount))

	liquid := int64(account.Balance) - totalReserve
	if liquid < 0 {
		return 0
	}
	return liquid
}

// insufficientBalance checks if the account has insufficient balance for the amount.
// Reference: rippled AMMCreate.cpp line 153-163
func insufficientBalance(view tx.LedgerView, accountID [20]byte, amount tx.Amount, xrpLiquid int64) bool {
	if amount.IsNative() {
		return xrpLiquid < amount.Drops()
	}

	// For IOU, check if account is issuer (issuers have unlimited balance)
	issuerID, err := sle.DecodeAccountID(amount.Issuer)
	if err != nil {
		return true
	}
	if accountID == issuerID {
		return false
	}

	// Check account holds sufficient amount (zero if frozen)
	held := tx.AccountFunds(view, accountID, amount, true, 0, 0)
	return held.Compare(amount) < 0
}

// isLPToken checks if the amount is an LP token (issued by an AMM account).
// Reference: rippled AMMCreate.cpp line 172-177
func isLPToken(view tx.LedgerView, amount tx.Amount) bool {
	// XRP is not an LP token
	if amount.IsNative() {
		return false
	}

	// Check if the issuer account has sfAMMID field (meaning it's an AMM pseudo-account)
	issuerID, err := sle.DecodeAccountID(amount.Issuer)
	if err != nil {
		return false
	}

	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		return false
	}

	issuerAccount, err := sle.ParseAccountRoot(issuerData)
	if err != nil {
		return false
	}

	// AMM accounts have the lsfAMM flag set
	return (issuerAccount.Flags & sle.LsfAMM) != 0
}

// sortAssets returns assets and amounts in canonical order (minmax).
// Reference: rippled AMMCreate.cpp line 262-264
func sortAssets(asset1, asset2 tx.Asset, amount1, amount2 tx.Amount) (tx.Asset, tx.Asset, tx.Amount, tx.Amount) {
	// Compare by currency first, then by issuer
	if compareAssets(asset1, asset2) <= 0 {
		return asset1, asset2, amount1, amount2
	}
	return asset2, asset1, amount2, amount1
}

// compareAssets compares two assets for canonical ordering.
// XRP < IOU, then by currency, then by issuer.
func compareAssets(a, b tx.Asset) int {
	aIsXRP := a.Currency == "" || a.Currency == "XRP"
	bIsXRP := b.Currency == "" || b.Currency == "XRP"

	// XRP comes first
	if aIsXRP && !bIsXRP {
		return -1
	}
	if !aIsXRP && bIsXRP {
		return 1
	}
	if aIsXRP && bIsXRP {
		return 0
	}

	// Both are IOU - compare currency
	if a.Currency < b.Currency {
		return -1
	}
	if a.Currency > b.Currency {
		return 1
	}

	// Same currency - compare issuer
	if a.Issuer < b.Issuer {
		return -1
	}
	if a.Issuer > b.Issuer {
		return 1
	}

	return 0
}

// setAMMNodeFlag sets the lsfAMMNode flag on the AMM's trustline for an IOU asset.
// Reference: rippled AMMCreate.cpp sendAndTrustSet line 297-306
func setAMMNodeFlag(ammAccountID [20]byte, asset tx.Asset, view tx.LedgerView) error {
	issuerID, err := sle.DecodeAccountID(asset.Issuer)
	if err != nil {
		return err
	}

	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return err
	}

	rs, err := sle.ParseRippleState(trustLineData)
	if err != nil {
		return err
	}

	// Set lsfAMMNode flag
	rs.Flags |= sle.LsfAMMNode

	// Serialize and update
	rsBytes, err := sle.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	return view.Update(trustLineKey, rsBytes)
}
