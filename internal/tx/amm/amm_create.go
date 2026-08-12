package amm

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

func (a *AMMCreate) TxType() tx.Type {
	return tx.TypeAMMCreate
}

// GetAmountAsset returns the issue (currency + issuer) of the first asset (Amount field).
// Implements ammCreateIssueProvider for the ValidAMM invariant checker.
func (a *AMMCreate) GetAmountAsset() tx.Asset {
	return amountAsset(a.Amount)
}

// GetAmount2Asset returns the issue (currency + issuer) of the second asset (Amount2 field).
// Implements ammCreateIssueProvider for the ValidAMM invariant checker.
func (a *AMMCreate) GetAmount2Asset() tx.Asset {
	return amountAsset(a.Amount2)
}

// Reference: rippled AMMCreate.cpp preflight
// GetFlagsMask adopts the engine FlagsMasker seam. AMMCreate defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (a *AMMCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tfAMMCreateMask
}

func (a *AMMCreate) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Reference: rippled AMMCreate.cpp line 52-57
	if matchesAssetByIssue(amountAsset(a.Amount), amountAsset(a.Amount2)) {
		return ter.Errorf(ter.TemBAD_AMM_TOKENS, "tokens can not have the same currency/issuer")
	}

	// Validate amounts using invalidAMMAmount logic. The error code
	// (temBAD_CURRENCY / temBAD_ISSUER / temBAD_AMOUNT) is propagated unchanged.
	// Reference: rippled AMMCreate.cpp line 59-69
	if err := validateAMMAmount(a.Amount); err != nil {
		return err
	}
	if err := validateAMMAmount(a.Amount2); err != nil {
		return err
	}

	// TradingFee must be 0-1000 (0-1%)
	if a.TradingFee > tradingFeeThreshold {
		return ter.Errorf(ter.TemBAD_FEE, "TradingFee must be 0-1000")
	}

	return nil
}

func (a *AMMCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// CalculateBaseFee returns the minimum fee for AMMCreate transactions.
// AMMCreate requires one owner reserve as the fee (not the standard base fee).
// Reference: rippled AMMCreate.cpp calculateBaseFee — returns view.fees().increment
func (a *AMMCreate) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return config.ReserveIncrement
}

func (a *AMMCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM}
}

// CheckExtraFeatures gates MPT pool assets on the MPTokensV2 amendment.
func (a *AMMCreate) CheckExtraFeatures(rules *amendment.Rules) error {
	return requireMPTokensV2(rules, a.Amount.IsMPT() || a.Amount2.IsMPT())
}

// Preclaim validates AMM uniqueness, authorization, freeze, DefaultRipple, the
// LP-token-trustline reserve, funding, that neither asset is an LP token, the
// pseudo-account collision (featureSingleAssetVault), and the clawback gate.
// The view's source-account balance is already the pre-fee balance.
// Reference: rippled AMMCreate.cpp preclaim
func (a *AMMCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(a.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	account, result := readAccount(view, accountID)
	if result != ter.TesSUCCESS {
		return result
	}

	asset1 := amountAsset(a.Amount)
	asset2 := amountAsset(a.Amount2)

	// Reference: rippled AMMCreate.cpp line 95-100
	ammKey := computeAMMKeylet(asset1, asset2)
	if exists, _ := view.Exists(ammKey); exists {
		return ter.TecDUPLICATE
	}

	// Reference: rippled AMMCreate.cpp line 102-116
	if result := requireAssetAuth(view, asset1, accountID, true, config.ParentCloseTime); result != ter.TesSUCCESS {
		return result
	}
	if result := requireAssetAuth(view, asset2, accountID, true, config.ParentCloseTime); result != ter.TesSUCCESS {
		return result
	}

	// Reference: rippled AMMCreate.cpp line 119-124
	if assetFrozen(view, accountID, asset1) {
		return frozenAssetResult(asset1)
	}
	if assetFrozen(view, accountID, asset2) {
		return frozenAssetResult(asset2)
	}

	// Reference: rippled AMMCreate.cpp line 126-142
	if noDefaultRipple(view, asset1) || noDefaultRipple(view, asset2) {
		return ter.TerNO_RIPPLE
	}

	// Check reserve for the LP token trustline, then sufficient funding for both
	// assets against the liquid (post-reserve) XRP balance.
	// Reference: rippled AMMCreate.cpp line 145-170
	if insufficientLPTokenReserve(view, account, config) {
		return TecINSUF_RESERVE_LINE
	}
	reserveNeeded, ok := tx.AccountReserveForView(view, config, account, tx.ConfineOwnerCount(account.OwnerCount, 1))
	if !ok {
		return ter.TefINTERNAL
	}
	xrpLiquid := int64(account.Balance) - int64(reserveNeeded)
	insufficient, result := insufficientBalance(view, accountID, a.Amount, xrpLiquid)
	if result != ter.TesSUCCESS {
		return result
	}
	if insufficient {
		return TecUNFUNDED_AMM
	}
	insufficient, result = insufficientBalance(view, accountID, a.Amount2, xrpLiquid)
	if result != ter.TesSUCCESS {
		return result
	}
	if insufficient {
		return TecUNFUNDED_AMM
	}

	// Reference: rippled AMMCreate.cpp line 172-184
	if isLPToken(view, a.Amount) || isLPToken(view, a.Amount2) {
		return ter.TecAMM_INVALID_TOKENS
	}

	// Check for pseudo-account collision with featureSingleAssetVault. This
	// runs before the clawback check to match rippled's preclaim ordering: a
	// collision returns terADDRESS_COLLISION (retried, no fee) rather than the
	// clawback tecNO_PERMISSION (claimed, fee consumed).
	// Reference: rippled AMMCreate.cpp preclaim lines 186-192
	if config.RequireRules().Enabled(amendment.FeatureSingleAssetVault) {
		if tx.PseudoAccountAddress(view, config.ParentHash, ammKey.Key) == ([20]byte{}) {
			return ter.TerADDRESS_COLLISION
		}
	}

	if result := canMPTTradeAndTransfer(view, asset1, accountID, accountID); result != ter.TesSUCCESS {
		return result
	}
	if result := canMPTTradeAndTransfer(view, asset2, accountID, accountID); result != ter.TesSUCCESS {
		return result
	}

	// Check clawback - if featureAMMClawback is not enabled, reject clawback-enabled issuers.
	// Reference: rippled AMMCreate.cpp preclaim lines 194-214
	if !config.RequireRules().Enabled(amendment.FeatureAMMClawback) {
		if result := clawbackDisabled(view, asset1); result != ter.TesSUCCESS {
			return result
		}
		if result := clawbackDisabled(view, asset2); result != ter.TesSUCCESS {
			return result
		}
	}

	return ter.TesSUCCESS
}

// Reference: rippled AMMCreate.cpp applyCreate
func (a *AMMCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("amm create apply",
		"account", a.Account,
		"amount", a.Amount,
		"amount2", a.Amount2,
		"tradingFee", a.TradingFee,
	)

	accountID := ctx.AccountID
	math := newNumberMath(ctx)

	asset1 := amountAsset(a.Amount)
	asset2 := amountAsset(a.Amount2)

	ammKey := computeAMMKeylet(asset1, asset2)

	// Create the AMM pseudo-account. This derives an unoccupied address, sets the
	// pseudo-account flags/sequence, marks it with sfAMMID, and inserts it; the
	// per-asset owner count and balances are layered on below.
	ammAccountID, ammAccount, res := tx.CreatePseudoAccount(ctx, ammKey.Key, tx.PseudoAMMID)
	if res != ter.TesSUCCESS {
		return res
	}
	ammAccountAddr := ammAccount.Account
	ammAccountKey := keylet.Account(ammAccountID)

	// Reference: rippled AMMCreate.cpp line 262-264
	sortedAsset1, sortedAsset2, sortedAmount1, sortedAmount2 := sortAssets(asset1, asset2, a.Amount, a.Amount2)

	lptCurrency := GenerateAMMLPTCurrencyForAssets(sortedAsset1, sortedAsset2)

	// Reference: rippled AMMCreate.cpp line 241-247
	lptIssuerID := ammAccountID
	lptKey := keylet.Line(accountID, lptIssuerID, lptCurrency)
	lptExists, _ := ctx.View.Exists(lptKey)
	if lptExists {
		return ter.TecDUPLICATE
	}

	// Initial LP token balance: sqrt(amount1 * amount2).
	// Reference: rippled AMMCreate.cpp line 256
	fixV1_3 := ctx.Rules().Enabled(amendment.FeatureFixAMMv1_3)
	lpTokenBalanceRaw := math.calculateLPTokens(sortedAmount1, sortedAmount2, fixV1_3)
	if lpTokenBalanceRaw.IsZero() {
		return ter.TecAMM_BALANCE
	}
	// Set the correct issue (currency + issuer) on the LP token balance.
	// The LP token currency is derived from the asset pair and the issuer
	// is the AMM pseudo-account.
	lpRounding := state.RoundToNearest
	if fixV1_3 {
		lpRounding = state.RoundDownward
	}
	lpTokenBalance := math.toAmount(
		lpTokenBalanceRaw,
		zeroAmount(tx.Asset{Currency: lptCurrency, Issuer: ammAccountAddr}),
		lpRounding,
	)

	// The AMM account's OwnerCount is the number of IOU pool trust lines it
	// holds (one per non-XRP asset): each is created with the reserve charged
	// to the AMM side. The AMM ledger object and any XRP side are not counted.
	ammOwnerCount := uint32(0)
	if !isXRPAsset(sortedAsset1) && !sortedAsset1.IsMPT() {
		ammOwnerCount++
	}
	if !isXRPAsset(sortedAsset2) && !sortedAsset2.IsMPT() {
		ammOwnerCount++
	}
	ammAccount.OwnerCount = ammOwnerCount

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

	creatorVote := VoteSlotData{
		Account:    accountID,
		TradingFee: a.TradingFee,
		VoteWeight: uint32(voteWeightScaleFactor),
	}
	ammData.VoteSlots = append(ammData.VoteSlots, creatorVote)

	expiration := ctx.Config.ParentCloseTime + uint32(totalTimeSlotSecs)
	discountedFee := uint16(0)
	if a.TradingFee > 0 {
		discountedFee = a.TradingFee / uint16(auctionSlotDiscountedFeeFraction)
	}
	ammData.AuctionSlot = &AuctionSlotData{
		Account:       accountID,
		Expiration:    expiration,
		Price:         zeroAmount(tx.Asset{Currency: lptCurrency, Issuer: ammAccountAddr}),
		DiscountedFee: discountedFee,
		AuthAccounts:  make([][20]byte, 0),
	}

	// Link the AMM entry into the AMM pseudo-account's owner directory and
	// record the page in sfOwnerNode. Without this the AMM account's owner
	// directory node is never created and the AMM SLE's OwnerNode is left
	// unset, diverging account_hash from rippled.
	// Reference: rippled AMMCreate.cpp:270 dirLink → View.cpp:1056-1064.
	ammOwnerDirKey := keylet.OwnerDir(ammAccountID)
	dirResult, err := state.DirInsert(ctx.View, ammOwnerDirKey, ammKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ammAccountID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	ammData.OwnerNode = dirResult.Page

	ammBytes, err := serializeAMMData(ammData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Insert(ammKey, ammBytes); err != nil {
		return ter.TefINTERNAL
	}

	// Reference: rippled AMMCreate.cpp line 278-283
	lptAsset := tx.Asset{
		Currency: lptCurrency,
		Issuer:   ammAccountAddr,
	}
	if err := createLPTokenTrustline(accountID, lptAsset, lpTokenBalance, ctx.View, ctx.NumberContext()); err != nil {
		return TecINSUF_RESERVE_LINE
	}

	// Transfer assets from creator to AMM and set lsfAMMNode on trustlines
	// Reference: rippled AMMCreate.cpp sendAndTrustSet lines 285-309
	isXRP1 := isXRPAsset(sortedAsset1)
	isXRP2 := isXRPAsset(sortedAsset2)

	// Reference: rippled AMMCreate.cpp sendAndTrustSet uses accountSend which
	// handles issuer-as-sender (no self-trust-line debit needed).
	if isXRP1 {
		drops := uint64(sortedAmount1.Drops())
		ctx.Account.Balance -= drops
		ammAccount.Balance += drops
	} else if sortedAsset1.IsMPT() {
		if result := requireAssetAuth(ctx.View, sortedAsset1, ammAccountID, false, ctx.Config.ParentCloseTime); result != ter.TesSUCCESS {
			return result
		}
		if result := ensureAMMMPTHolding(ctx.View, sortedAsset1, ammAccountID); result != ter.TesSUCCESS {
			return result
		}
		if result := sendMPT(ctx.View, accountID, ammAccountID, sortedAmount1, true); result != ter.TesSUCCESS {
			return result
		}
	} else {
		if err := createOrUpdateAMMTrustline(ammAccountID, sortedAsset1, sortedAmount1, ctx.View, ctx.NumberContext()); err != nil {
			return TecNO_LINE
		}
		if err := setAMMNodeFlag(ammAccountID, sortedAsset1, ctx.View); err != nil {
			return ter.TefINTERNAL
		}
		// Debit from creator's trustline (skip if creator is the issuer —
		// issuers have unlimited supply and no self-trust-line).
		issuerID1, _ := state.DecodeAccountID(sortedAsset1.Issuer)
		if accountID != issuerID1 {
			tlResult, tlErr := updateTrustlineBalanceInViewEx(accountID, issuerID1, sortedAsset1.Currency, sortedAmount1.Negate(), ctx.View, ctx.NumberContext())
			if tlErr != nil {
				return TecUNFUNDED_AMM
			}
			if tlResult.SenderOwnerCountDelta != 0 {
				if err := tx.DecreaseOwnerCount(ctx.View, ctx.Account, tlResult.SenderSponsor, 1); err != nil {
					return ter.TefINTERNAL
				}
			}
			// If deleting the creator's trust line also cleared the issuer's
			// reserve on that line, decrement the issuer's owner count.
			if tlResult.IssuerOwnerCountDelta != 0 {
				if err := tx.DecreaseOwnerCountOnView(ctx.View, issuerID1, tlResult.IssuerSponsor, 1); err != nil {
					return ter.TefINTERNAL
				}
				ctx.SyncSenderSponsorCounts(tlResult.IssuerSponsor)
			}
		}
	}

	if isXRP2 {
		drops := uint64(sortedAmount2.Drops())
		ctx.Account.Balance -= drops
		ammAccount.Balance += drops
	} else if sortedAsset2.IsMPT() {
		if result := requireAssetAuth(ctx.View, sortedAsset2, ammAccountID, false, ctx.Config.ParentCloseTime); result != ter.TesSUCCESS {
			return result
		}
		if result := ensureAMMMPTHolding(ctx.View, sortedAsset2, ammAccountID); result != ter.TesSUCCESS {
			return result
		}
		if result := sendMPT(ctx.View, accountID, ammAccountID, sortedAmount2, true); result != ter.TesSUCCESS {
			return result
		}
	} else {
		if err := createOrUpdateAMMTrustline(ammAccountID, sortedAsset2, sortedAmount2, ctx.View, ctx.NumberContext()); err != nil {
			return TecNO_LINE
		}
		if err := setAMMNodeFlag(ammAccountID, sortedAsset2, ctx.View); err != nil {
			return ter.TefINTERNAL
		}
		issuerID2, _ := state.DecodeAccountID(sortedAsset2.Issuer)
		if accountID != issuerID2 {
			tlResult, tlErr := updateTrustlineBalanceInViewEx(accountID, issuerID2, sortedAsset2.Currency, sortedAmount2.Negate(), ctx.View, ctx.NumberContext())
			if tlErr != nil {
				return TecUNFUNDED_AMM
			}
			if tlResult.SenderOwnerCountDelta != 0 {
				if err := tx.DecreaseOwnerCount(ctx.View, ctx.Account, tlResult.SenderSponsor, 1); err != nil {
					return ter.TefINTERNAL
				}
			}
			// If deleting the creator's trust line also cleared the issuer's
			// reserve on that line, decrement the issuer's owner count.
			if tlResult.IssuerOwnerCountDelta != 0 {
				if err := tx.DecreaseOwnerCountOnView(ctx.View, issuerID2, tlResult.IssuerSponsor, 1); err != nil {
					return ter.TefINTERNAL
				}
				ctx.SyncSenderSponsorCounts(tlResult.IssuerSponsor)
			}
		}
	}

	// Update creator account owner count:
	// +1 for the LP token trustline, plus any adjustments from IOU trust line
	// deletion (when the creator deposits all their IOU balance, the original
	// trust line may be deleted, decrementing owner count).
	ctx.Account.OwnerCount = tx.ConfineOwnerCount(ctx.Account.OwnerCount, 1)

	accountKey := keylet.Account(accountID)
	accountBytes, err := state.SerializeAccountRoot(ctx.Account)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(accountKey, accountBytes); err != nil {
		return ter.TefINTERNAL
	}

	ammAccountBytes, err := state.SerializeAccountRoot(ammAccount)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(ammAccountKey, ammAccountBytes); err != nil {
		return ter.TefINTERNAL
	}

	ctx.Log.Debug("amm create: success",
		"ammAccount", ammAccountAddr,
		"lpTokens", lpTokenBalance,
		"amount", sortedAmount1,
		"amount2", sortedAmount2,
	)

	return ter.TesSUCCESS
}

// sortAssets returns assets and amounts in canonical order, mirroring rippled's
// std::minmax(amount.issue(), amount2.issue()): the pair is ordered by 20-byte
// currency code then, for non-XRP, by 20-byte issuer AccountID, keeping the
// original order on an equivalent comparison.
func sortAssets(asset1, asset2 tx.Asset, amount1, amount2 tx.Amount) (tx.Asset, tx.Asset, tx.Amount, tx.Amount) {
	if assetLessEqual(asset1, asset2) {
		return asset1, asset2, amount1, amount2
	}
	return asset2, asset1, amount2, amount1
}

// assetLessEqual reports whether asset a sorts at-or-before asset b under
// rippled's Issue ordering, comparing decoded currency and issuer bytes rather
// than their string representations (base58 r-addresses and ISO-vs-hex currency
// codes do not sort the same as their decoded bytes).
func assetLessEqual(a, b tx.Asset) bool {
	if a.IsMPT() != b.IsMPT() {
		return a.IsMPT()
	}
	if a.IsMPT() {
		aID, _ := decodeMPTAsset(a)
		bID, _ := decodeMPTAsset(b)
		return bytes.Compare(aID[:], bID[:]) <= 0
	}
	return keylet.IssueLessEqual(
		keylet.CurrencyBytes(a.Currency), getIssuerBytes(a.Issuer),
		keylet.CurrencyBytes(b.Currency), getIssuerBytes(b.Issuer),
	)
}
