package check

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// CheckCash cashes a Check, drawing from the sender's balance.
type CheckCash struct {
	tx.BaseTx

	// CheckID is the ID of the check to cash (required)
	CheckID string `json:"CheckID" xrpl:"CheckID"`

	// Amount is the exact amount to receive (optional, mutually exclusive with DeliverMin)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`

	// DeliverMin is the minimum amount to receive (optional, mutually exclusive with Amount)
	DeliverMin *tx.Amount `json:"DeliverMin,omitempty" xrpl:"DeliverMin,omitempty,amount"`
}

// NewCheckCash creates a new CheckCash transaction
func NewCheckCash(account, checkID string) *CheckCash {
	return &CheckCash{
		BaseTx:  *tx.NewBaseTx(tx.TypeCheckCash, account),
		CheckID: checkID,
	}
}

func (c *CheckCash) TxType() tx.Type {
	return tx.TypeCheckCash
}

// Validate implements preflight validation matching rippled's CashCheck::preflight().
// GetFlagsMask adopts the engine FlagsMasker seam. CashCheck defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (c *CheckCash) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (c *CheckCash) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	if c.CheckID == "" {
		return ter.Errorf(ter.TemMALFORMED, "CheckID is required")
	}

	// Must have exactly one of Amount or DeliverMin
	// Reference: CashCheck.cpp L52-62
	hasAmount := c.Amount != nil
	hasDeliverMin := c.DeliverMin != nil

	if hasAmount == hasDeliverMin {
		return ter.Errorf(ter.TemMALFORMED, "must specify exactly one of Amount or DeliverMin")
	}

	// Validate the provided amount
	// Reference: CashCheck.cpp L65-77
	if hasAmount {
		if !tx.IsLegalNetAmount(*c.Amount) || c.Amount.Signum() <= 0 {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
		}
		if badMPTAsset(*c.Amount) {
			return ter.Errorf(ter.TemBAD_CURRENCY, "invalid MPT issuance")
		}
		if !c.Amount.IsNative() && c.Amount.Currency == "XRP" {
			return ter.Errorf(ter.TemBAD_CURRENCY, "invalid currency")
		}
	}

	if hasDeliverMin {
		if !tx.IsLegalNetAmount(*c.DeliverMin) || c.DeliverMin.Signum() <= 0 {
			return ter.Errorf(ter.TemBAD_AMOUNT, "DeliverMin must be positive")
		}
		if badMPTAsset(*c.DeliverMin) {
			return ter.Errorf(ter.TemBAD_CURRENCY, "invalid MPT issuance")
		}
		if !c.DeliverMin.IsNative() && c.DeliverMin.Currency == "XRP" {
			return ter.Errorf(ter.TemBAD_CURRENCY, "invalid currency")
		}
	}

	return nil
}

func (c *CheckCash) PreflightWithRules(rules *amendment.Rules) error {
	if rules.FixCleanup3_3_0Enabled() && isZeroCheckID(c.CheckID) {
		return ter.Errorf(ter.TemMALFORMED, "CheckID must not be zero")
	}
	return c.Validate()
}

func (c *CheckCash) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// SetExactAmount sets the exact amount to receive
func (c *CheckCash) SetExactAmount(amount tx.Amount) {
	c.Amount = &amount
	c.DeliverMin = nil
}

// SetDeliverMin sets the minimum amount to receive
func (c *CheckCash) SetDeliverMin(amount tx.Amount) {
	c.DeliverMin = &amount
	c.Amount = nil
}

func (c *CheckCash) RequiredAmendments() [][32]byte {
	return nil
}

func (c *CheckCash) Preclaim(view tx.ReadOnlyLedgerView, config tx.EngineConfig) ter.Result {
	checkIDBytes, err := hex.DecodeString(c.CheckID)
	if err != nil || len(checkIDBytes) != 32 {
		return ter.TemINVALID
	}

	var checkKeyBytes [32]byte
	copy(checkKeyBytes[:], checkIDBytes)
	checkKey := keylet.Keylet{Key: checkKeyBytes}
	checkData, err := view.Read(checkKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if checkData == nil {
		return ter.TecNO_ENTRY
	}
	checkType, err := state.DecodeType(checkData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if checkType != entry.TypeCheck {
		return ter.TecNO_ENTRY
	}
	check, err := state.ParseCheck(checkData)
	if err != nil {
		return ter.TefINTERNAL
	}
	accountID, err := state.DecodeAccountID(c.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	if check.DestinationID != accountID {
		return ter.TecNO_PERMISSION
	}
	if check.Account == check.DestinationID {
		return ter.TecINTERNAL
	}

	sourceData, err := view.Read(keylet.Account(check.Account))
	if err != nil {
		return ter.TefINTERNAL
	}
	if sourceData == nil {
		return ter.TecNO_ENTRY
	}
	sourceAccount, err := state.ParseAccountRoot(sourceData)
	if err != nil {
		return ter.TefINTERNAL
	}
	destinationData, err := view.Read(keylet.Account(accountID))
	if err != nil {
		return ter.TefINTERNAL
	}
	if destinationData == nil {
		return ter.TecNO_ENTRY
	}
	destinationAccount, err := state.ParseAccountRoot(destinationData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if destinationAccount.Flags&state.LsfRequireDestTag != 0 && !check.HasDestTag {
		return ter.TecDST_TAG_NEEDED
	}
	if tx.HasExpiredField(check.Expiration, config.ParentCloseTime) {
		return ter.TecEXPIRED
	}

	value := c.Amount
	if value == nil {
		value = c.DeliverMin
	}
	if value == nil {
		return ter.TemMALFORMED
	}
	rules := view.Rules()
	if rules == nil {
		rules = config.Rules
	}
	if rules != nil && rules.FixCleanup3_2_0Enabled() && !legalStoredMPTAmount(check.SendMaxAmount) {
		return ter.TefBAD_LEDGER
	}
	if result := matchesCheckSendMax(*value, check.SendMaxAmount); result != ter.TesSUCCESS {
		return result
	}
	if value.IsMPT() {
		requested, ok := value.MPTRaw()
		if !ok || requested <= 0 {
			return ter.TemBAD_AMOUNT
		}
		sendMax, ok := check.SendMaxAmount.MPTRaw()
		if !ok || requested > sendMax {
			return ter.TecPATH_PARTIAL
		}
	} else {
		cmp, err := value.CompareChecked(check.SendMaxAmount)
		if err != nil {
			return ter.TemMALFORMED
		}
		if cmp > 0 {
			return ter.TecPATH_PARTIAL
		}
	}

	if result := checkCashSourceFunds(view, config, sourceAccount, check.Account, *value, checkData); result != ter.TesSUCCESS {
		return result
	}

	if value.IsNative() {
		return ter.TesSUCCESS
	}
	if value.IsMPT() {
		return checkCashPreclaimMPTDestination(view, config, check.Account, accountID, *value)
	}
	return checkCashPreclaimIOUDestination(view, accountID, *value)
}

func checkCashSourceFunds(view tx.ReadOnlyLedgerView, config tx.EngineConfig, sourceAccount *state.AccountRoot, sourceID [20]byte, value tx.Amount, checkData []byte) ter.Result {
	if value.IsNative() {
		sponsor, err := tx.LedgerEntrySponsor(checkData, "Sponsor")
		if err != nil {
			return ter.TefINTERNAL
		}
		reserve, ok := tx.AccountReserveForView(view, config, sourceAccount, sourceAccount.OwnerCount)
		if !ok {
			return ter.TefINTERNAL
		}
		var available uint64
		if sourceAccount.Balance > reserve {
			available = sourceAccount.Balance - reserve
		}
		if sponsor == "" {
			if ^uint64(0)-available < config.ReserveIncrement {
				return ter.TefINTERNAL
			}
			available += config.ReserveIncrement
		}
		if value.Drops() < 0 || uint64(value.Drops()) > available {
			return ter.TecPATH_PARTIAL
		}
		return ter.TesSUCCESS
	}

	if value.IsMPT() {
		mptID, err := mptutil.DecodeID(value.MPTIssuanceID())
		if err != nil {
			return ter.TecOBJECT_NOT_FOUND
		}
		if result := mptutil.RequireAuthAt(view, mptID, sourceID, true, config.ParentCloseTime); result != ter.TesSUCCESS {
			if result == ter.TefINTERNAL {
				return result
			}
			return ter.TecPATH_PARTIAL
		}
		requested, ok := value.MPTRaw()
		if !ok || requested < 0 {
			return ter.TemBAD_AMOUNT
		}
		funds, result := mptutil.Funds(view, mptID, sourceID, true)
		if result == ter.TefINTERNAL {
			return result
		}
		if result != ter.TesSUCCESS || funds < requested {
			return ter.TecPATH_PARTIAL
		}
		return ter.TesSUCCESS
	}

	issuerID, err := state.DecodeAccountID(value.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}
	if sourceID == issuerID {
		return ter.TesSUCCESS
	}
	frozen, err := tx.IsIOUFrozen(view, sourceID, issuerID, value.Currency)
	if err != nil {
		return ter.TefINTERNAL
	}
	if frozen || tx.IsDeepFrozen(view, sourceID, issuerID, value.Currency) {
		return ter.TecPATH_PARTIAL
	}
	if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureFixFrozenLPTokenTransfer) {
		switch tx.LPTokenFrozenForIssuer(view, sourceID, issuerID) {
		case tx.LPTokenFrozen, tx.LPTokenAMMUnresolvable:
			return ter.TecPATH_PARTIAL
		}
	}
	funds, err := tx.AccountFundsNoFreezeStrict(view, sourceID, value, config.ReserveBase, config.ReserveIncrement)
	if err != nil {
		return ter.TefINTERNAL
	}
	cmp, err := value.CompareChecked(funds)
	if err != nil {
		return ter.TefINTERNAL
	}
	if cmp > 0 {
		return ter.TecPATH_PARTIAL
	}
	return ter.TesSUCCESS
}

func checkCashPreclaimIOUDestination(view tx.ReadOnlyLedgerView, destinationID [20]byte, value tx.Amount) ter.Result {
	issuerID, err := state.DecodeAccountID(value.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuerID == destinationID {
		return ter.TesSUCCESS
	}
	trustLineKey := keylet.Line(destinationID, issuerID, value.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	issuerData, err := view.Read(keylet.Account(issuerID))
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuerData == nil {
		return ter.TecNO_ISSUER
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuerAccount.Flags&state.LsfRequireAuth != 0 {
		if trustLineData == nil {
			return ter.TecNO_AUTH
		}
		trustLine, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return ter.TefINTERNAL
		}
		authFlag := state.LsfHighAuth
		if state.CompareAccountIDs(destinationID, issuerID) > 0 {
			authFlag = state.LsfLowAuth
		}
		if trustLine.Flags&authFlag == 0 {
			return ter.TecNO_AUTH
		}
	}
	frozen, err := tx.IsIOUFrozen(view, destinationID, issuerID, value.Currency)
	if err != nil {
		return ter.TefINTERNAL
	}
	if frozen {
		return ter.TecFROZEN
	}
	return ter.TesSUCCESS
}

func checkCashPreclaimMPTDestination(view tx.ReadOnlyLedgerView, config tx.EngineConfig, sourceID, destinationID [20]byte, value tx.Amount) ter.Result {
	mptID, err := mptutil.DecodeID(value.MPTIssuanceID())
	if err != nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	issuerID := mptutil.Issuer(mptID)
	if issuerID == destinationID {
		return ter.TesSUCCESS
	}
	issuerData, err := view.Read(keylet.Account(issuerID))
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuerData == nil {
		return ter.TecNO_ISSUER
	}
	if _, err := state.ParseAccountRoot(issuerData); err != nil {
		return ter.TefINTERNAL
	}
	if result := mptutil.RequireAuthAt(view, mptID, destinationID, false, config.ParentCloseTime); result != ter.TesSUCCESS {
		return result
	}
	if mptutil.IsFrozen(view, mptID, destinationID) {
		return ter.TecLOCKED
	}
	return mptutil.CanTransfer(view, mptID, sourceID, destinationID)
}

// CheckExtraFeatures gates an MPT-denominated Amount or DeliverMin on the
// MPTokensV2 amendment, mirroring rippled CheckCash::checkExtraFeatures.
func (c *CheckCash) CheckExtraFeatures(rules *amendment.Rules) error {
	if rules.MPTokensV2Enabled() {
		return nil
	}
	if (c.Amount != nil && c.Amount.IsMPT()) || (c.DeliverMin != nil && c.DeliverMin.IsMPT()) {
		return ter.Errorf(ter.TemDISABLED, "MPT amounts require MPTokensV2 amendment")
	}
	return nil
}

func (c *CheckCash) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("check cash apply",
		"account", c.Account,
		"checkID", c.CheckID,
		"amount", c.Amount,
		"deliverMin", c.DeliverMin,
	)

	// Parse check ID
	checkIDBytes, err := hex.DecodeString(c.CheckID)
	if err != nil || len(checkIDBytes) != 32 {
		return ter.TemINVALID
	}

	var checkKeyBytes [32]byte
	copy(checkKeyBytes[:], checkIDBytes)
	checkKey := keylet.Keylet{Key: checkKeyBytes}

	// Read check
	// Reference: CashCheck.cpp L85-90
	checkData, err := ctx.View.Read(checkKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if checkData == nil {
		return ter.TecNO_ENTRY
	}

	// View.Read is untyped, so reject a CheckID that resolves to a non-Check
	// object, matching rippled's tecNO_ENTRY.
	checkType, err := state.DecodeType(checkData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if checkType != entry.TypeCheck {
		return ter.TecNO_ENTRY
	}

	// Parse check
	check, err := state.ParseCheck(checkData)
	if err != nil {
		return ter.TefINTERNAL
	}

	accountID := ctx.AccountID

	// Read source (check writer) and destination accounts. If the check
	// exists, both should be present; a missing one is a corrupt ledger.
	srcKey := keylet.Account(check.Account)
	srcData, err := ctx.View.Read(srcKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if srcData == nil {
		return ter.TecNO_ENTRY
	}

	destKey := keylet.Account(accountID)
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if destData == nil {
		return ter.TecNO_ENTRY
	}
	// Determine the cash amount
	if c.Amount != nil {
		return c.applyCashWithAmount(ctx, check, checkKey)
	}
	return c.applyCashWithDeliverMin(ctx, check, checkKey)
}

func legalStoredMPTAmount(amount state.Amount) bool {
	if !amount.IsMPT() {
		return true
	}
	value, ok := amount.MPTRaw()
	return ok && value >= 0
}

// matchesCheckSendMax reports whether the requested cash amount's currency and
// issuer match the check's SendMax. A mismatch returns temMALFORMED, matching
// rippled's preclaim where the currency is compared before the issuer.
// XRP and an issued currency never match (their currencies differ).
// Reference: CashCheck.cpp L144-155.
func matchesCheckSendMax(value, sendMax state.Amount) ter.Result {
	if value.IsMPT() || sendMax.IsMPT() {
		if !value.IsMPT() || !sendMax.IsMPT() ||
			!strings.EqualFold(value.MPTIssuanceID(), sendMax.MPTIssuanceID()) {
			return ter.TemMALFORMED
		}
		return ter.TesSUCCESS
	}
	if value.IsNative() != sendMax.IsNative() {
		return ter.TemMALFORMED
	}
	if value.IsNative() {
		return ter.TesSUCCESS
	}
	if value.Currency != sendMax.Currency {
		return ter.TemMALFORMED
	}
	if value.Issuer != sendMax.Issuer {
		return ter.TemMALFORMED
	}
	return ter.TesSUCCESS
}

// applyCashWithAmount handles the exact Amount case for both XRP and IOU.
func (c *CheckCash) applyCashWithAmount(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet) ter.Result {
	amount := c.Amount

	// For XRP checks
	if amount.IsNative() {
		return c.applyCashXRP(ctx, check, checkKey, uint64(amount.Drops()), false)
	}
	if amount.IsMPT() {
		return c.applyCashMPTAmount(ctx, check, checkKey, *amount, false)
	}

	// IOU Amount
	return c.applyCashIOUAmount(ctx, check, checkKey, *amount, false)
}

// applyCashWithDeliverMin handles the DeliverMin case for both XRP and IOU.
func (c *CheckCash) applyCashWithDeliverMin(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet) ter.Result {
	deliverMin := c.DeliverMin

	// For XRP checks
	if deliverMin.IsNative() {
		return c.applyCashXRP(ctx, check, checkKey, uint64(deliverMin.Drops()), true)
	}
	if deliverMin.IsMPT() {
		return c.applyCashMPTAmount(ctx, check, checkKey, *deliverMin, true)
	}

	// IOU DeliverMin
	return c.applyCashIOUAmount(ctx, check, checkKey, *deliverMin, true)
}

// applyCashXRP handles XRP check cashing for both the exact Amount and the
// DeliverMin paths. requestedDrops is the Amount (exact) or DeliverMin
// (minimum); isDeliverMin selects which. rippled handles both in one path:
// after the funds checks it delivers min(srcLiquid, SendMax) for DeliverMin and
// exactly the requested drops for Amount. Reference: CashCheck.cpp L294-334.
func (c *CheckCash) applyCashXRP(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet, requestedDrops uint64, isDeliverMin bool) ter.Result {
	creatorKey := keylet.Account(check.Account)
	creatorData, err := ctx.View.Read(creatorKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	creatorAccount, err := state.ParseAccountRoot(creatorData)
	if err != nil {
		return ter.TefINTERNAL
	}

	sponsorAddress, err := tx.LedgerEntrySponsorFromView(ctx.View, checkKey, "Sponsor")
	if err != nil {
		return ter.TefINTERNAL
	}
	sponsored := sponsorAddress != ""

	srcLiquid := xrpLiquidAfterCheck(creatorAccount, ctx, sponsored)
	if srcLiquid < requestedDrops {
		return ter.TecUNFUNDED_PAYMENT
	}

	// For DeliverMin, deliver as much as possible up to SendMax; for an exact
	// Amount, deliver exactly the requested drops.
	cashAmount := requestedDrops
	if isDeliverMin {
		cashAmount = min(srcLiquid, check.SendMax)

		// Set delivered_amount metadata for the DeliverMin XRP path.
		deliveredAmt := tx.NewXRPAmount(int64(cashAmount))
		ctx.Metadata.DeliveredAmount = &deliveredAmt
	}

	// Transfer XRP
	creatorAccount.Balance -= cashAmount
	ctx.Account.Balance += cashAmount

	// Remove check from directories before erasing
	if result := removeCheckFromDirectories(ctx, check, checkKey.Key); result != ter.TesSUCCESS {
		return result
	}

	if err := tx.DecreaseOwnerCount(ctx.View, creatorAccount, sponsorAddress, 1); err != nil {
		return ter.TefINTERNAL
	}
	ctx.SyncSenderSponsorCounts(sponsorAddress)

	// Update creator account
	if result := ctx.UpdateAccountRoot(check.Account, creatorAccount); result != ter.TesSUCCESS {
		return result
	}

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// xrpLiquidAfterCheck returns the writer's zero-clamped liquid XRP computed with
// the check's reserve already released (owner count minus one), matching
// rippled's xrpLiquid(psb, srcId, -1). This is the amount actually available to
// fund the transfer in doApply. Reference: CashCheck.cpp L304, View.cpp xrpLiquid.
func xrpLiquidAfterCheck(creator *state.AccountRoot, ctx *tx.ApplyContext, sponsored bool) uint64 {
	ownerCount := creator.OwnerCount
	if !sponsored && ownerCount > 0 {
		ownerCount--
	}
	reserve := ctx.AccountReserveFor(creator, ownerCount)
	if creator.Balance > reserve {
		return creator.Balance - reserve
	}
	return 0
}

func (c *CheckCash) applyCashMPTAmount(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet, requestedAmount tx.Amount, isDeliverMin bool) ter.Result {
	mptID, err := mptutil.DecodeID(requestedAmount.MPTIssuanceID())
	if err != nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	requested, ok := requestedAmount.MPTRaw()
	if !ok || requested <= 0 {
		return ter.TemBAD_AMOUNT
	}
	sendMax, ok := check.SendMaxAmount.MPTRaw()
	if !ok {
		return ter.TefBAD_LEDGER
	}

	issuance, _, result := mptutil.ReadIssuance(ctx.View, mptID)
	if result != ter.TesSUCCESS {
		if result == ter.TecOBJECT_NOT_FOUND {
			return ter.TecPATH_PARTIAL
		}
		return result
	}
	srcID := check.Account
	dstID := ctx.AccountID
	issuerID := issuance.Issuer

	srcFunds, result := mptutil.Funds(ctx.View, mptID, srcID, true)
	if result == ter.TefINTERNAL {
		return result
	}
	if result != ter.TesSUCCESS || srcFunds < requested {
		return ter.TecPATH_PARTIAL
	}

	if dstID != issuerID {
		if exists, err := ctx.View.Exists(keylet.MPTokenByID(mptID, dstID)); err != nil {
			return ter.TefINTERNAL
		} else if !exists {
			if result := ctx.CheckReserveFor(ctx.AccountID, ctx.Account, ctx.PriorBalance(), 1, 0, ter.TecINSUFFICIENT_RESERVE); result != ter.TesSUCCESS {
				return result
			}
			if result := mptutil.EnsureHoldingForContext(ctx, mptID, dstID, 0); result != ter.TesSUCCESS {
				return result
			}
		}
	}

	rate := mptutil.RateOne
	if srcID != issuerID && dstID != issuerID {
		rate = mptutil.TransferRate(ctx.View, mptID)
	}
	maxDebit := min(sendMax, srcFunds)
	maximum := state.NewMPTAmountWithIssuanceID(maxDebit, "", requestedAmount.MPTIssuanceID())
	maxDelivery, _ := maximum.MulRatio(mptutil.RateOne, rate, false).MPTRaw()
	if requested > maxDelivery {
		return ter.TecPATH_PARTIAL
	}
	delivered := requested
	if isDeliverMin {
		delivered = maxDelivery
	}
	deliveredAmount := state.NewMPTAmountWithIssuanceID(delivered, "", requestedAmount.MPTIssuanceID())
	debit, _ := deliveredAmount.MulRatio(rate, mptutil.RateOne, true).MPTRaw()
	if srcID == issuerID || dstID == issuerID {
		result = mptutil.Credit(ctx.View, mptID, srcID, dstID, delivered, false)
	} else {
		result = mptutil.Credit(ctx.View, mptID, issuerID, dstID, delivered, true)
		if result == ter.TesSUCCESS {
			result = mptutil.Credit(ctx.View, mptID, srcID, issuerID, debit, false)
		}
	}
	if result == ter.TecINSUFFICIENT_FUNDS || result == ter.TecPATH_DRY {
		return ter.TecPATH_PARTIAL
	}
	if result != ter.TesSUCCESS {
		return result
	}
	ctx.Metadata.DeliveredAmount = &deliveredAmount

	if result := removeCheckFromDirectories(ctx, check, checkKey.Key); result != ter.TesSUCCESS {
		return result
	}
	creatorData, err := ctx.View.Read(keylet.Account(srcID))
	if err != nil || creatorData == nil {
		return ter.TefINTERNAL
	}
	creatorAccount, err := state.ParseAccountRoot(creatorData)
	if err != nil {
		return ter.TefINTERNAL
	}
	sponsorAddress, err := tx.LedgerEntrySponsorFromView(ctx.View, checkKey, "Sponsor")
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := tx.DecreaseOwnerCount(ctx.View, creatorAccount, sponsorAddress, 1); err != nil {
		return ter.TefINTERNAL
	}
	ctx.SyncSenderSponsorCounts(sponsorAddress)
	if result := ctx.UpdateAccountRoot(srcID, creatorAccount); result != ter.TesSUCCESS {
		return result
	}
	if err := ctx.View.Erase(checkKey); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// applyCashIOUAmount handles IOU check cashing for both Amount and DeliverMin.
// When isDeliverMin is true, the requestedAmount is treated as the minimum and
// the flow engine delivers as much as possible up to SendMax.
// Reference: CashCheck.cpp L252-end
func (c *CheckCash) applyCashIOUAmount(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet, requestedAmount tx.Amount, isDeliverMin bool) ter.Result {
	accountID := ctx.AccountID
	sendMax := check.SendMaxAmount

	issuerID, err := state.DecodeAccountID(sendMax.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}

	srcID := check.Account

	// Auto-create the destination's trust line if it does not yet exist.
	destLow := state.CompareAccountIDs(issuerID, accountID) > 0
	trustLineAccountID := checkCashTrustLineAccount(srcID, accountID, issuerID)

	if accountID != issuerID {
		trustLineKey := keylet.Line(accountID, issuerID, sendMax.Currency)
		trustLineExists, err := ctx.View.Exists(trustLineKey)
		if err != nil {
			return ter.TefINTERNAL
		}

		if !trustLineExists {
			// Check reserve for creating trust line
			// Reference: CashCheck.cpp L373-378
			priorBalance := ctx.PriorBalance()
			if result := ctx.CheckReserveFor(ctx.AccountID, ctx.Account, priorBalance, 1, 0, ter.TecNO_LINE_INSUF_RESERVE); result != ter.TesSUCCESS {
				return ter.TecNO_LINE_INSUF_RESERVE
			}

			// Create trust line
			if result := createTrustLineForCheckCash(ctx, accountID, issuerID, sendMax.Currency); result != ter.TesSUCCESS {
				return result
			}
		}
	}

	var savedLimit state.Amount
	tweakLimit := accountID != issuerID || !ctx.Rules().FixCleanup3_4_0Enabled()
	if tweakLimit {
		// The casher accepts funds above the holder limit; restore it after flow.
		trustLineKey := keylet.Line(trustLineAccountID, issuerID, sendMax.Currency)
		trustLineData, err := ctx.View.Read(trustLineKey)
		if err != nil {
			return ter.TefINTERNAL
		}
		if trustLineData == nil {
			return ter.TecNO_LINE
		}
		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return ter.TefINTERNAL
		}

		bigLimit := state.NewIssuedAmountFromValue(state.MaxMantissa, state.MaxExponent, sendMax.Currency, sendMax.Issuer)
		if destLow {
			savedLimit = rs.LowLimit
			rs.LowLimit = bigLimit
		} else {
			savedLimit = rs.HighLimit
			rs.HighLimit = bigLimit
		}

		updatedData, err := state.SerializeRippleState(rs)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := ctx.View.Update(trustLineKey, updatedData); err != nil {
			return ter.TefINTERNAL
		}
	}

	// Determine flow parameters
	var flowAmount tx.Amount // What to deliver
	if isDeliverMin {
		// For DeliverMin, request delivery of SendMax (maximum possible)
		flowAmount = sendMax
	} else {
		// For exact Amount, deliver exactly what was requested
		flowAmount = requestedAmount
	}

	// Execute flow using RippleCalculate
	// Reference: CashCheck.cpp L442-455
	rc := payment.RippleCalculate(
		ctx.View,
		srcID,        // source (check creator)
		accountID,    // destination (check casher)
		flowAmount,   // amount to deliver
		&sendMax,     // SendMax as input limit
		nil,          // no explicit paths
		true,         // use default path
		isDeliverMin, // partial payment for DeliverMin
		false,        // no limit quality
		ctx.TxHash,
		ctx.Config.LedgerSequence,
		payment.WithNumberContext(ctx.NumberContext()),
		payment.WithOffersConsidered(ctx.Config.OffersConsidered),
	)
	actualOut, sandbox, flowResult := rc.ActualOut, rc.Sandbox, rc.Result

	if flowResult != ter.TesSUCCESS && flowResult != ter.TecPATH_PARTIAL {
		ctx.Log.Warn("check cash: flow failed", "result", flowResult)
		// Restore the trust line limit before returning
		if tweakLimit {
			if result := restoreTrustLineLimit(ctx, trustLineAccountID, issuerID, sendMax.Currency, destLow, savedLimit); result != ter.TesSUCCESS {
				return result
			}
		}
		return flowResult
	}

	// For DeliverMin, check that actual output >= deliverMin
	// Reference: CashCheck.cpp L463-475
	if isDeliverMin {
		actualOutAmount := payment.FromEitherAmount(actualOut)
		if actualOutAmount.Compare(requestedAmount) < 0 {
			ctx.Log.Warn("check cash: flow did not produce DeliverMin", "actual", actualOutAmount, "deliverMin", requestedAmount)
			// Restore the trust line limit before returning
			if tweakLimit {
				if result := restoreTrustLineLimit(ctx, trustLineAccountID, issuerID, sendMax.Currency, destLow, savedLimit); result != ter.TesSUCCESS {
					return result
				}
			}
			return ter.TecPATH_PARTIAL
		}
	}

	// For exact Amount, flow must have succeeded
	if !isDeliverMin && flowResult != ter.TesSUCCESS {
		// Restore the trust line limit before returning
		if tweakLimit {
			if result := restoreTrustLineLimit(ctx, trustLineAccountID, issuerID, sendMax.Currency, destLow, savedLimit); result != ter.TesSUCCESS {
				return result
			}
		}
		return ter.TecPATH_PARTIAL
	}

	// Apply flow sandbox changes
	if sandbox != nil {
		if err := sandbox.ApplyToView(ctx.View); err != nil {
			return ter.TefINTERNAL
		}
	}

	// Restore the trust line limit after applying flow changes.
	// The flow engine may have modified the balance, but we need to
	// restore the original limit that was tweaked.
	// Reference: CashCheck.cpp scope_exit at L426-429
	if tweakLimit {
		if result := restoreTrustLineLimit(ctx, trustLineAccountID, issuerID, sendMax.Currency, destLow, savedLimit); result != ter.TesSUCCESS {
			return result
		}
	}

	// Set the delivered amount metadata in all cases, not just for DeliverMin.
	deliveredAmt := payment.FromEitherAmount(actualOut)
	ctx.Metadata.DeliveredAmount = &deliveredAmt

	// Remove check from directories before erasing.
	// Reference: CashCheck.cpp L487-508
	if result := removeCheckFromDirectories(ctx, check, checkKey.Key); result != ter.TesSUCCESS {
		return result
	}

	// Decrease creator's owner count and delete the check
	creatorKey := keylet.Account(srcID)
	creatorData, err := ctx.View.Read(creatorKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	creatorAccount, err := state.ParseAccountRoot(creatorData)
	if err != nil {
		return ter.TefINTERNAL
	}

	sponsorAddress, err := tx.LedgerEntrySponsorFromView(ctx.View, checkKey, "Sponsor")
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := tx.DecreaseOwnerCount(ctx.View, creatorAccount, sponsorAddress, 1); err != nil {
		return ter.TefINTERNAL
	}
	ctx.SyncSenderSponsorCounts(sponsorAddress)

	if result := ctx.UpdateAccountRoot(srcID, creatorAccount); result != ter.TesSUCCESS {
		return result
	}

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// createTrustLineForCheckCash creates a trust line between the check casher
// (destination) and the issuer when the CheckCashMakesTrustLine amendment is
// enabled, delegating to the shared tx.TrustCreate. The casher is the account
// being set (it pays the reserve); the issuer is the peer. The casher is the
// transaction sender, so its OwnerCount is bumped on ctx.Account, which the
// engine writes back.
// Reference: CashCheck.cpp L349-412, View.cpp trustCreate L1329-1445
func createTrustLineForCheckCash(ctx *tx.ApplyContext, destID, issuerID [20]byte, currency string) ter.Result {
	trustLineKey := keylet.Line(destID, issuerID, currency)

	destStr, err := state.EncodeAccountID(destID)
	if err != nil {
		return ter.TefINTERNAL
	}

	// The account-being-set's (casher's) noRipple is derived from its own
	// lsfDefaultRipple, exactly as rippled's check trustCreate call.
	// Reference: rippled CashCheck.cpp:393 (sleDst->getFlags() & lsfDefaultRipple) == 0.
	destData, err := ctx.View.Read(keylet.Account(destID))
	if err != nil {
		return ter.TefINTERNAL
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return ter.TefINTERNAL
	}
	destNoRipple := destAccount.Flags&state.LsfDefaultRipple == 0

	destLow := state.CompareAccountIDs(destID, issuerID) < 0

	sponsorAddress, result := tx.IncreaseOwnerCount(ctx, ctx.AccountID, ctx.Account, 1)
	if result != ter.TesSUCCESS {
		return result
	}
	result = tx.TrustCreate(ctx.View, tx.TrustCreateParams{
		SrcHigh:     destLow,
		Src:         issuerID,
		Dst:         destID,
		LineKey:     trustLineKey,
		LimitIssuer: destID,
		NoRipple:    destNoRipple,
		Balance:     state.NewIssuedAmountFromValue(0, state.MinExponent, currency, state.AccountOneAddress),
		Limit:       state.NewIssuedAmountFromValue(0, state.MinExponent, currency, destStr),
		Sponsor:     sponsorAddress,
	})
	if result != ter.TesSUCCESS {
		return result
	}

	return ter.TesSUCCESS
}

func checkCashTrustLineAccount(srcID, destID, issuerID [20]byte) [20]byte {
	if destID == issuerID {
		return srcID
	}
	return destID
}

// restoreTrustLineLimit restores the original trust line limit after flow.
// Reference: CashCheck.cpp scope_exit at L426-429
func restoreTrustLineLimit(ctx *tx.ApplyContext, lineAccountID, issuerID [20]byte, currency string, destLow bool, savedLimit state.Amount) ter.Result {
	trustLineKey := keylet.Line(lineAccountID, issuerID, currency)
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if trustLineData == nil {
		return ter.TesSUCCESS
	}
	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return ter.TefINTERNAL
	}

	if destLow {
		rs.LowLimit = savedLimit
	} else {
		rs.HighLimit = savedLimit
	}

	updatedData, err := state.SerializeRippleState(rs)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(trustLineKey, updatedData); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// removeCheckFromDirectories removes a check from both source and destination
// owner directories. Must be called before erasing the check SLE.
// Reference: CashCheck.cpp L487-508
func removeCheckFromDirectories(ctx *tx.ApplyContext, check *state.CheckData, checkKeyBytes [32]byte) ter.Result {
	srcID := check.Account
	dstID := check.DestinationID

	// Remove from destination directory (if not self-send)
	if srcID != dstID {
		destDirKey := keylet.OwnerDir(dstID)
		if result := tx.DirRemoveOrBadLedger(ctx.View, destDirKey, check.DestinationNode, checkKeyBytes); result != ter.TesSUCCESS {
			return result
		}
	}

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(srcID)
	return tx.DirRemoveOrBadLedger(ctx.View, ownerDirKey, check.OwnerNode, checkKeyBytes)
}
