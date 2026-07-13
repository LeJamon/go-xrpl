package vault

import (
	"bytes"
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// assetMPTID decodes an MPT asset's 24-byte issuance ID.
func assetMPTID(a tx.Asset) ([24]byte, bool) {
	var id [24]byte
	b, err := hex.DecodeString(a.MPTIssuanceID)
	if err != nil || len(b) != 24 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// mptIDIssuer returns the 20-byte issuer embedded in a 24-byte MPT issuance ID.
func mptIDIssuer(id [24]byte) [20]byte {
	var issuer [20]byte
	copy(issuer[:], id[4:])
	return issuer
}

// readMPTIssuance reads and parses an MPT issuance, returning (nil, nil) when
// absent.
func readMPTIssuance(view tx.LedgerView, id [24]byte) (*state.MPTokenIssuanceData, error) {
	data, err := view.Read(keylet.MPTIssuance(id))
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	return state.ParseMPTokenIssuance(data)
}

// canAddHolding checks whether accountID could hold asset: XRP/IOU via
// canAddHoldingIssue, MPT via issuance existence + lsfMPTCanTransfer.
func canAddHolding(view tx.LedgerView, asset tx.Asset) ter.Result {
	if !asset.IsMPT() {
		return canAddHoldingIssue(view, asset)
	}
	id, ok := assetMPTID(asset)
	if !ok {
		return ter.TemMALFORMED
	}
	iss, err := readMPTIssuance(view, id)
	if err != nil {
		return ter.TefINTERNAL
	}
	if iss == nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	if iss.Flags&entry.LsfMPTCanTransfer == 0 {
		return ter.TecNO_AUTH
	}
	return ter.TesSUCCESS
}

// addEmptyMPTHolding creates a zero-balance MPToken for accountID under the MPT
// asset (nothing when the account is the issuer). Returns the owner-count delta.
func addEmptyMPTHolding(ctx *tx.ApplyContext, accountID [20]byte, asset tx.Asset) (int32, ter.Result) {
	id, ok := assetMPTID(asset)
	if !ok {
		return 0, ter.TefINTERNAL
	}
	if mptIDIssuer(id) == accountID {
		return 0, ter.TesSUCCESS
	}
	tokenKey := keylet.MPTokenByID(id, accountID)
	if exists, _ := ctx.View.Exists(tokenKey); exists {
		return 0, ter.TecDUPLICATE
	}
	token := &state.MPTokenData{Account: accountID, MPTokenIssuanceID: id}
	dir, err := state.DirInsert(ctx.View, keylet.OwnerDir(accountID), tokenKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = accountID
	})
	if err != nil {
		return 0, ter.TecDIR_FULL
	}
	token.OwnerNode = dir.Page
	data, serr := state.SerializeMPToken(token)
	if serr != nil {
		return 0, ter.TefINTERNAL
	}
	if ierr := ctx.View.Insert(tokenKey, data); ierr != nil {
		return 0, ter.TefINTERNAL
	}
	return 1, ter.TesSUCCESS
}

// sendMPTAsset moves amount of the MPT asset from `from` to `to`, crediting or
// debiting OutstandingAmount when either party is the issuer.
func sendMPTAsset(ctx *tx.ApplyContext, mptID [24]byte, from, to [20]byte, amount uint64) ter.Result {
	issuanceKey := keylet.MPTIssuance(mptID)
	issData, err := ctx.View.Read(issuanceKey)
	if err != nil || len(issData) == 0 {
		return ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(issData)
	if perr != nil {
		return ter.TefINTERNAL
	}
	issuerID := mptIDIssuer(mptID)

	if from == issuerID {
		// The issuer is putting tokens into circulation: the new outstanding
		// supply must not exceed MaximumAmount (default 2^63-1). Callers that
		// disburse to several destinations issue one sendMPTAsset per leg and
		// commit OutstandingAmount between them, so this per-leg cap on the
		// freshly read supply enforces the aggregate cap across the whole
		// disbursement (rippled's rippleSendMultiMPT aggregate check; the
		// fixCleanup3_1_3 gate there only refines multi-leg precision, and the
		// single-leg cap in rippleSendMPT is unconditional).
		maxAmount := maxMPTokenAmount
		if issuance.MaximumAmount != nil {
			maxAmount = *issuance.MaximumAmount
		}
		if amount > maxAmount || issuance.OutstandingAmount > maxAmount-amount {
			return ter.TecPATH_DRY
		}
		issuance.OutstandingAmount += amount
	} else {
		tokenKey := keylet.MPTokenByID(mptID, from)
		token, terr := readMPToken(ctx.View, tokenKey)
		if terr != nil || token == nil {
			return ter.TecNO_AUTH
		}
		if token.MPTAmount < amount {
			return ter.TecINSUFFICIENT_FUNDS
		}
		token.MPTAmount -= amount
		data, serr := state.SerializeMPToken(token)
		if serr != nil {
			return ter.TefINTERNAL
		}
		if uerr := ctx.View.Update(tokenKey, data); uerr != nil {
			return ter.TefINTERNAL
		}
	}

	if to == issuerID {
		if issuance.OutstandingAmount < amount {
			return ter.TefINTERNAL
		}
		issuance.OutstandingAmount -= amount
	} else {
		tokenKey := keylet.MPTokenByID(mptID, to)
		token, terr := readMPToken(ctx.View, tokenKey)
		if terr != nil || token == nil {
			return ter.TecNO_AUTH
		}
		token.MPTAmount += amount
		data, serr := state.SerializeMPToken(token)
		if serr != nil {
			return ter.TefINTERNAL
		}
		if uerr := ctx.View.Update(tokenKey, data); uerr != nil {
			return ter.TefINTERNAL
		}
	}

	updated, serr := state.SerializeMPTokenIssuance(issuance)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(issuanceKey, updated); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// canAddHoldingIssue mirrors rippled's canAddHolding for an IOU/XRP asset: XRP is
// always addable; an IOU issuer must exist and have DefaultRipple set.
func canAddHoldingIssue(view tx.LedgerView, asset tx.Asset) ter.Result {
	if isNativeAsset(asset) {
		return ter.TesSUCCESS
	}
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return ter.TerNO_ACCOUNT
	}
	ar, err := tx.ReadAccountRoot(view, issuerID)
	if err != nil {
		return ter.TefINTERNAL
	}
	if ar == nil {
		return ter.TerNO_ACCOUNT
	}
	if ar.Flags&state.LsfDefaultRipple == 0 {
		return ter.TerNO_RIPPLE
	}
	return ter.TesSUCCESS
}

// addEmptyHolding gives accountID a zero-balance holding for asset: nothing for
// XRP or when the account is the issuer, and a no-ripple trust line for an IOU.
// The account SLE must already exist in the view. Returns the owner-count delta
// the caller should apply to the holding account (1 when a line was created).
// Reference: rippled View.cpp addEmptyHolding (Issue overload).
func addEmptyHolding(ctx *tx.ApplyContext, accountID [20]byte, asset tx.Asset) (int32, ter.Result) {
	if isNativeAsset(asset) {
		return 0, ter.TesSUCCESS
	}
	if asset.IsMPT() {
		return addEmptyMPTHolding(ctx, accountID, asset)
	}
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return 0, ter.TefINTERNAL
	}
	if accountID == issuerID {
		return 0, ter.TesSUCCESS
	}
	if tx.IsGlobalFrozen(ctx.View, asset.Issuer) {
		return 0, ter.TecFROZEN
	}

	lineKey := keylet.Line(issuerID, accountID, asset.Currency)
	if exists, _ := ctx.View.Exists(lineKey); exists {
		return 0, ter.TecDUPLICATE
	}

	holder, err := tx.ReadAccountRoot(ctx.View, accountID)
	if err != nil || holder == nil {
		return 0, ter.TefINTERNAL
	}
	if ctx.PriorBalance() < ctx.AccountReserve(holder.OwnerCount+1) {
		return 0, ter.TecNO_LINE_INSUF_RESERVE
	}

	holderAddr, err := state.EncodeAccountID(accountID)
	if err != nil {
		return 0, ter.TefINTERNAL
	}
	holderLow := bytes.Compare(accountID[:], issuerID[:]) < 0
	res := tx.TrustCreate(ctx.View, tx.TrustCreateParams{
		SrcHigh:     holderLow,
		Src:         issuerID,
		Dst:         accountID,
		LineKey:     lineKey,
		LimitIssuer: accountID,
		NoRipple:    true,
		Balance:     state.NewIssuedAmountFromValue(0, state.MinExponent, asset.Currency, state.AccountOneAddress),
		Limit:       tx.NewIssuedAmount(0, state.MinExponent, asset.Currency, holderAddr),
	})
	if res != ter.TesSUCCESS {
		return 0, res
	}
	return 1, ter.TesSUCCESS
}

// readVault reads and parses the vault ledger entry at vaultKey, returning
// (nil, nil) when the entry does not exist.
func readVault(view AssetReadView, vaultKey keylet.Keylet) (*vaultData, error) {
	data, err := view.Read(vaultKey)
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	return parseVault(data)
}

// readMPToken reads and parses an MPToken, returning (nil, nil) when absent.
func readMPToken(view tx.LedgerView, tokenKey keylet.Keylet) (*state.MPTokenData, error) {
	data, err := view.Read(tokenKey)
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	return state.ParseMPToken(data)
}

// isSoleShareholder reports whether account holds every outstanding share of the
// vault, so it owns both the available and the future value.
func isSoleShareholder(view tx.LedgerView, account [20]byte, shareMPTID [24]byte, outstanding uint64) bool {
	if outstanding == 0 {
		return false
	}
	token, err := readMPToken(view, keylet.MPTokenByID(shareMPTID, account))
	if err != nil || token == nil {
		return false
	}
	return token.MPTAmount == outstanding
}

// ensureHolderMPToken creates a zero-balance MPToken for holderID under the
// share issuance if one does not already exist, charging the reserve and owner
// count against the holder (which is ctx.Account when it is the tx submitter).
func ensureHolderMPToken(ctx *tx.ApplyContext, holderID [20]byte, shareMPTID [24]byte) ter.Result {
	tokenKey := keylet.MPTokenByID(shareMPTID, holderID)
	if exists, _ := ctx.View.Exists(tokenKey); exists {
		return ter.TesSUCCESS
	}

	isSubmitter := holderID == ctx.AccountID
	var ownerCount uint32
	if isSubmitter {
		ownerCount = ctx.Account.OwnerCount
	} else {
		ar, err := tx.ReadAccountRoot(ctx.View, holderID)
		if err != nil || ar == nil {
			return ter.TefINTERNAL
		}
		ownerCount = ar.OwnerCount
	}
	if ctx.PriorBalance() < ctx.ReserveForNewObject(ownerCount) {
		return ter.TecINSUFFICIENT_RESERVE
	}

	token := &state.MPTokenData{Account: holderID, MPTokenIssuanceID: shareMPTID}
	ownerDirKey := keylet.OwnerDir(holderID)
	dir, err := state.DirInsert(ctx.View, ownerDirKey, tokenKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = holderID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	token.OwnerNode = dir.Page
	data, err := state.SerializeMPToken(token)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Insert(tokenKey, data); err != nil {
		return ter.TefINTERNAL
	}

	if isSubmitter {
		ctx.Account.OwnerCount++
	} else if err := tx.AdjustOwnerCount(ctx.View, holderID, 1); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// authorizeHolderMPToken sets lsfMPTAuthorized on a holder's share MPToken (the
// issuer-side authorization the pseudo-account grants a private vault's owner).
func authorizeHolderMPToken(ctx *tx.ApplyContext, holderID [20]byte, shareMPTID [24]byte) ter.Result {
	tokenKey := keylet.MPTokenByID(shareMPTID, holderID)
	token, err := readMPToken(ctx.View, tokenKey)
	if err != nil || token == nil {
		return ter.TefINTERNAL
	}
	token.Flags |= entry.LsfMPTAuthorized
	data, serr := state.SerializeMPToken(token)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(tokenKey, data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// vaultAssetOf returns the vault's asset as a tx.Asset (XRP, IOU, or MPT).
func vaultAssetOf(vd *vaultData) tx.Asset {
	if vd.AssetIsMPT {
		return tx.Asset{MPTIssuanceID: hex.EncodeToString(vd.AssetMPTID[:])}
	}
	return vd.Asset
}

// canWithdraw checks that a withdrawal of amount from `from` may be delivered to
// `to`: the destination must exist, satisfy any RequireDestTag / DepositAuth
// requirement, and (for an IOU delivered to a third party) not exceed its trust
// limit. Reference: rippled View.cpp canWithdraw.
func canWithdraw(view tx.LedgerView, from, to [20]byte, amount tx.Amount, hasDestTag bool) ter.Result {
	toAcct, err := tx.ReadAccountRoot(view, to)
	if err != nil {
		return ter.TefINTERNAL
	}
	if toAcct == nil {
		return ter.TecNO_DST
	}
	if toAcct.Flags&state.LsfRequireDestTag != 0 && !hasDestTag {
		return ter.TecDST_TAG_NEEDED
	}
	if from == to {
		return ter.TesSUCCESS
	}
	if toAcct.Flags&state.LsfDepositAuth != 0 {
		if exists, _ := view.Exists(keylet.DepositPreauth(to, from)); !exists {
			return ter.TecNO_PERMISSION
		}
	}
	return withdrawToDestExceedsLimit(view, from, to, amount)
}

// withdrawToDestExceedsLimit rejects an IOU withdrawal that would push the
// third-party destination past its trust limit. XRP and MPT are exempt.
func withdrawToDestExceedsLimit(view tx.LedgerView, from, to [20]byte, amount tx.Amount) ter.Result {
	if amount.IsNative() || amount.IsMPT() {
		return ter.TesSUCCESS
	}
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}
	if from == to || to == issuerID {
		return ter.TesSUCCESS
	}
	owed := lineBalanceInTerms(view, to, issuerID, amount.Currency)
	if owed.Signum() <= 0 {
		limit := lineLimit(view, to, issuerID, amount.Currency)
		amountN, aerr := amountToNumber(amount)
		if aerr != nil {
			return ter.TefINTERNAL
		}
		negOwed := owed.Negate()
		if negOwed.Cmp(limit) >= 0 || amountN.Cmp(limit.Add(owed)) > 0 {
			return ter.TecNO_LINE
		}
	}
	return ter.TesSUCCESS
}

// lineBalanceInTerms returns the trust-line balance between account and issuer
// expressed in account's terms (positive means the account holds the asset).
func lineBalanceInTerms(view tx.LedgerView, account, issuer [20]byte, currency string) state.XRPLNumber {
	data, err := view.Read(keylet.Line(account, issuer, currency))
	if err != nil || len(data) == 0 {
		return newVaultNumber(0, 0)
	}
	rs, perr := state.ParseRippleState(data)
	if perr != nil {
		return newVaultNumber(0, 0)
	}
	bal, berr := vaultNumber(rs.Balance.Value())
	if berr != nil {
		return newVaultNumber(0, 0)
	}
	if bytes.Compare(account[:], issuer[:]) > 0 {
		bal = bal.Negate()
	}
	return bal
}

// lineLimit returns account's own trust limit toward issuer for currency.
func lineLimit(view tx.LedgerView, account, issuer [20]byte, currency string) state.XRPLNumber {
	data, err := view.Read(keylet.Line(account, issuer, currency))
	if err != nil || len(data) == 0 {
		return newVaultNumber(0, 0)
	}
	rs, perr := state.ParseRippleState(data)
	if perr != nil {
		return newVaultNumber(0, 0)
	}
	var lim state.Amount
	if bytes.Compare(account[:], issuer[:]) < 0 {
		lim = rs.LowLimit
	} else {
		lim = rs.HighLimit
	}
	n, nerr := vaultNumber(lim.Value())
	if nerr != nil {
		return newVaultNumber(0, 0)
	}
	return n
}

// assetMatches reports whether amount denominates the vault's asset.
func assetMatches(amount tx.Amount, vd *vaultData) bool {
	if vd.AssetIsMPT {
		return amount.IsMPT() &&
			strings.EqualFold(amount.MPTIssuanceID(), hex.EncodeToString(vd.AssetMPTID[:]))
	}
	if isNativeAsset(vd.Asset) {
		return amount.IsNative()
	}
	return !amount.IsNative() && !amount.IsMPT() &&
		amount.Currency == vd.Asset.Currency && amount.Issuer == vd.Asset.Issuer
}

// spendableAsset returns how much of asset accountID can spend, mirroring
// accountHolds(shFULL_BALANCE): the full XRP or trust-line balance, treated as
// effectively unbounded when the account is the asset's issuer.
func spendableAsset(view tx.LedgerView, config tx.EngineConfig, accountID [20]byte, asset tx.Asset) (state.XRPLNumber, error) {
	if isNativeAsset(asset) {
		ar, err := tx.ReadAccountRoot(view, accountID)
		if err != nil || ar == nil {
			return newVaultNumber(0, 0), err
		}
		reserve := config.AccountReserve(ar.OwnerCount)
		liquid := int64(ar.Balance) - int64(reserve)
		if liquid < 0 {
			liquid = 0
		}
		return newVaultNumber(liquid, 0), nil
	}

	if asset.IsMPT() {
		id, ok := assetMPTID(asset)
		if !ok {
			return newVaultNumber(0, 0), nil
		}
		if mptIDIssuer(id) == accountID {
			iss, _ := readMPTIssuance(view, id)
			if iss == nil {
				return newVaultNumber(0, 0), nil
			}
			maxAmt := entry.MaxMPTokenAmount
			if iss.MaximumAmount != nil {
				maxAmt = *iss.MaximumAmount
			}
			return newVaultNumber(int64(maxAmt-iss.OutstandingAmount), 0), nil
		}
		return newVaultNumber(int64(holderMPTBalance(view, id, accountID)), 0), nil
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return newVaultNumber(0, 0), err
	}
	if accountID == issuerID {
		// The issuer's spendable balance is effectively unbounded.
		return newVaultNumber(9999999999999999, 80), nil
	}

	lineData, rerr := view.Read(keylet.Line(accountID, issuerID, asset.Currency))
	if rerr != nil || lineData == nil {
		return newVaultNumber(0, 0), nil
	}
	rs, perr := state.ParseRippleState(lineData)
	if perr != nil {
		return newVaultNumber(0, 0), perr
	}
	bal, berr := vaultNumber(rs.Balance.Value())
	if berr != nil {
		return newVaultNumber(0, 0), berr
	}
	// Balance is stored in the low account's terms; negate for the high account.
	if bytes.Compare(accountID[:], issuerID[:]) > 0 {
		bal = bal.Negate()
	}
	return bal, nil
}

// sendAssetToVault transfers assetsN of the vault asset from the submitter
// (ctx.Account) to the vault pseudo-account.
func sendAssetToVault(ctx *tx.ApplyContext, vaultAccountID [20]byte, orig tx.Amount, assetsN state.XRPLNumber) ter.Result {
	if orig.IsNative() {
		drops := uint64(assetsN.ToInt64WithMode(state.RoundTowardsZero))
		if ctx.Account.Balance < drops {
			return ter.TecINSUFFICIENT_FUNDS
		}
		ctx.Account.Balance -= drops
		vaultAcct, err := tx.ReadAccountRoot(ctx.View, vaultAccountID)
		if err != nil || vaultAcct == nil {
			return ter.TefINTERNAL
		}
		vaultAcct.Balance += drops
		data, serr := state.SerializeAccountRoot(vaultAcct)
		if serr != nil {
			return ter.TefINTERNAL
		}
		if uerr := ctx.View.Update(keylet.Account(vaultAccountID), data); uerr != nil {
			return ter.TefINTERNAL
		}
		return ter.TesSUCCESS
	}

	if orig.IsMPT() {
		id, ok := decodeMPTID(orig.MPTIssuanceID())
		if !ok {
			return ter.TefINTERNAL
		}
		amount := uint64(assetsN.ToInt64WithMode(state.RoundTowardsZero))
		return sendMPTAsset(ctx, id, ctx.AccountID, vaultAccountID, amount)
	}

	amt := state.NewIssuedAmountFromValue(assetsN.Mantissa(), assetsN.Exponent(), orig.Currency, orig.Issuer)
	return tx.RippleSendIOU(ctx.View, ctx.AccountID, vaultAccountID, amt, true)
}

// decodeMPTID decodes a 48-char hex MPT issuance ID.
func decodeMPTID(s string) ([24]byte, bool) {
	var id [24]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 24 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// sendAssetFromVault transfers assetsN of the vault asset from the vault
// pseudo-account to dstID.
func sendAssetFromVault(ctx *tx.ApplyContext, vaultAccountID, dstID [20]byte, asset tx.Asset, assetsN state.XRPLNumber) ter.Result {
	if isNativeAsset(asset) {
		drops := uint64(assetsN.ToInt64WithMode(state.RoundTowardsZero))
		vaultAcct, err := tx.ReadAccountRoot(ctx.View, vaultAccountID)
		if err != nil || vaultAcct == nil {
			return ter.TefINTERNAL
		}
		if vaultAcct.Balance < drops {
			return ter.TecINSUFFICIENT_FUNDS
		}
		vaultAcct.Balance -= drops
		data, serr := state.SerializeAccountRoot(vaultAcct)
		if serr != nil {
			return ter.TefINTERNAL
		}
		if uerr := ctx.View.Update(keylet.Account(vaultAccountID), data); uerr != nil {
			return ter.TefINTERNAL
		}
		if dstID == ctx.AccountID {
			ctx.Account.Balance += drops
		} else {
			dst, derr := tx.ReadAccountRoot(ctx.View, dstID)
			if derr != nil || dst == nil {
				return ter.TefINTERNAL
			}
			dst.Balance += drops
			ddata, serr := state.SerializeAccountRoot(dst)
			if serr != nil {
				return ter.TefINTERNAL
			}
			if uerr := ctx.View.Update(keylet.Account(dstID), ddata); uerr != nil {
				return ter.TefINTERNAL
			}
		}
		return ter.TesSUCCESS
	}

	if asset.IsMPT() {
		id, ok := assetMPTID(asset)
		if !ok {
			return ter.TefINTERNAL
		}
		amount := uint64(assetsN.ToInt64WithMode(state.RoundTowardsZero))
		return sendMPTAsset(ctx, id, vaultAccountID, dstID, amount)
	}

	amt := state.NewIssuedAmountFromValue(assetsN.Mantissa(), assetsN.Exponent(), asset.Currency, asset.Issuer)
	return tx.RippleSendIOU(ctx.View, vaultAccountID, dstID, amt, true)
}

// burnShares decreases the share issuance's OutstandingAmount and debits the
// holder's share MPToken balance by shares.
func burnShares(ctx *tx.ApplyContext, shareMPTID [24]byte, holderID [20]byte, shares uint64) ter.Result {
	shareKey := keylet.MPTIssuance(shareMPTID)
	issData, err := ctx.View.Read(shareKey)
	if err != nil || issData == nil {
		return ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(issData)
	if perr != nil {
		return ter.TefINTERNAL
	}
	if issuance.OutstandingAmount < shares {
		return ter.TefINTERNAL
	}
	issuance.OutstandingAmount -= shares
	newIss, serr := state.SerializeMPTokenIssuance(issuance)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(shareKey, newIss); uerr != nil {
		return ter.TefINTERNAL
	}

	tokenKey := keylet.MPTokenByID(shareMPTID, holderID)
	token, terr := readMPToken(ctx.View, tokenKey)
	if terr != nil || token == nil {
		return ter.TefINTERNAL
	}
	if token.MPTAmount < shares {
		return ter.TecINSUFFICIENT_FUNDS
	}
	token.MPTAmount -= shares
	newTok, serr := state.SerializeMPToken(token)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(tokenKey, newTok); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// holderMPTBalance returns how many shares holderID holds under the share
// issuance (0 when the holder has no MPToken).
func holderMPTBalance(view tx.LedgerView, shareMPTID [24]byte, holderID [20]byte) uint64 {
	token, err := readMPToken(view, keylet.MPTokenByID(shareMPTID, holderID))
	if err != nil || token == nil {
		return 0
	}
	return token.MPTAmount
}

// removeVaultAssetHolding deletes the pseudo-account's trust line for an IOU
// vault asset (XRP needs no holding). Returns the owner-count delta to apply to
// the pseudo-account.
func removeVaultAssetHolding(ctx *tx.ApplyContext, accountID [20]byte, asset tx.Asset) (int32, ter.Result) {
	if isNativeAsset(asset) {
		return 0, ter.TesSUCCESS
	}
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return 0, ter.TefINTERNAL
	}
	if accountID == issuerID {
		return 0, ter.TesSUCCESS
	}
	lineKey := keylet.Line(accountID, issuerID, asset.Currency)
	data, rerr := ctx.View.Read(lineKey)
	if rerr != nil || data == nil {
		return 0, ter.TesSUCCESS
	}
	rs, perr := state.ParseRippleState(data)
	if perr != nil {
		return 0, ter.TefINTERNAL
	}

	lowID, highID := accountID, issuerID
	if bytes.Compare(accountID[:], issuerID[:]) > 0 {
		lowID, highID = issuerID, accountID
	}
	if res, e := state.DirRemove(ctx.View, keylet.OwnerDir(lowID), rs.LowNode, lineKey.Key, false); e != nil || !res.Success {
		return 0, ter.TefBAD_LEDGER
	}
	if res, e := state.DirRemove(ctx.View, keylet.OwnerDir(highID), rs.HighNode, lineKey.Key, false); e != nil || !res.Success {
		return 0, ter.TefBAD_LEDGER
	}
	if e := ctx.View.Erase(lineKey); e != nil {
		return 0, ter.TefINTERNAL
	}
	return -1, ter.TesSUCCESS
}

// removeEmptyShareMPToken deletes a holder's share MPToken when its balance is
// zero, returning tecHAS_OBLIGATIONS when it still holds shares.
func removeEmptyShareMPToken(ctx *tx.ApplyContext, holderID [20]byte, shareMPTID [24]byte) ter.Result {
	tokenKey := keylet.MPTokenByID(shareMPTID, holderID)
	token, err := readMPToken(ctx.View, tokenKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if token == nil {
		return ter.TesSUCCESS
	}
	if token.MPTAmount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}
	// A holding with escrow-locked shares can no longer be deleted: the lock is
	// an outstanding obligation. Gated on fixCleanup3_1_3.
	if ctx.Rules().Enabled(amendment.FeatureFixCleanup3_1_3) &&
		token.LockedAmount != nil && *token.LockedAmount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}
	if res, derr := state.DirRemove(ctx.View, keylet.OwnerDir(holderID), token.OwnerNode, tokenKey.Key, false); derr != nil || !res.Success {
		return ter.TefINTERNAL
	}
	if eerr := ctx.View.Erase(tokenKey); eerr != nil {
		return ter.TefINTERNAL
	}
	if holderID == ctx.AccountID {
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	} else if derr := tx.AdjustOwnerCount(ctx.View, holderID, -1); derr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// mintShares increases the share issuance's OutstandingAmount and credits the
// holder's share MPToken balance by shares.
func mintShares(ctx *tx.ApplyContext, shareMPTID [24]byte, holderID [20]byte, shares uint64) ter.Result {
	shareKey := keylet.MPTIssuance(shareMPTID)
	issData, err := ctx.View.Read(shareKey)
	if err != nil || issData == nil {
		return ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(issData)
	if perr != nil {
		return ter.TefINTERNAL
	}
	issuance.OutstandingAmount += shares
	newIss, serr := state.SerializeMPTokenIssuance(issuance)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(shareKey, newIss); uerr != nil {
		return ter.TefINTERNAL
	}

	tokenKey := keylet.MPTokenByID(shareMPTID, holderID)
	token, terr := readMPToken(ctx.View, tokenKey)
	if terr != nil || token == nil {
		return ter.TefINTERNAL
	}
	token.MPTAmount += shares
	newTok, serr := state.SerializeMPToken(token)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(tokenKey, newTok); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}
