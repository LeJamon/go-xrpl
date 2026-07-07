package vault

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// readAccountRoot reads and parses an AccountRoot, returning (nil, nil) when the
// account does not exist (view.Read reports a missing key via a nil payload).
func readAccountRoot(view tx.LedgerView, id [20]byte) (*state.AccountRoot, error) {
	data, err := view.Read(keylet.Account(id))
	if err != nil || data == nil {
		return nil, nil
	}
	return state.ParseAccountRoot(data)
}

// isPseudoAccountID reports whether id is an existing pseudo-account.
func isPseudoAccountID(view tx.LedgerView, id [20]byte) bool {
	ar, err := readAccountRoot(view, id)
	if err != nil || ar == nil {
		return false
	}
	return ar.IsPseudoAccount()
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
	ar, err := readAccountRoot(view, issuerID)
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

	holder, err := readAccountRoot(ctx.View, accountID)
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
func readVault(view tx.LedgerView, vaultKey keylet.Keylet) (*vaultData, error) {
	data, err := view.Read(vaultKey)
	if err != nil || data == nil {
		return nil, nil
	}
	return parseVault(data)
}

// readMPToken reads and parses an MPToken, returning (nil, nil) when absent.
func readMPToken(view tx.LedgerView, tokenKey keylet.Keylet) (*state.MPTokenData, error) {
	data, err := view.Read(tokenKey)
	if err != nil || data == nil {
		return nil, nil
	}
	return state.ParseMPToken(data)
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
		ar, err := readAccountRoot(ctx.View, holderID)
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

// vaultAssetOf returns the vault's asset as a tx.Asset (XRP or IOU).
func vaultAssetOf(vd *vaultData) tx.Asset {
	return vd.Asset
}

// canWithdraw checks that a withdrawal of amount from `from` may be delivered to
// `to`: the destination must exist, satisfy any RequireDestTag / DepositAuth
// requirement, and (for an IOU delivered to a third party) not exceed its trust
// limit. Reference: rippled View.cpp canWithdraw.
func canWithdraw(view tx.LedgerView, from, to [20]byte, amount tx.Amount, hasDestTag bool) ter.Result {
	toAcct, err := readAccountRoot(view, to)
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
	if err != nil || data == nil {
		return state.NewXRPLNumber(0, 0)
	}
	rs, perr := state.ParseRippleState(data)
	if perr != nil {
		return state.NewXRPLNumber(0, 0)
	}
	bal, berr := vaultNumber(rs.Balance.Value())
	if berr != nil {
		return state.NewXRPLNumber(0, 0)
	}
	if bytes.Compare(account[:], issuer[:]) > 0 {
		bal = bal.Negate()
	}
	return bal
}

// lineLimit returns account's own trust limit toward issuer for currency.
func lineLimit(view tx.LedgerView, account, issuer [20]byte, currency string) state.XRPLNumber {
	data, err := view.Read(keylet.Line(account, issuer, currency))
	if err != nil || data == nil {
		return state.NewXRPLNumber(0, 0)
	}
	rs, perr := state.ParseRippleState(data)
	if perr != nil {
		return state.NewXRPLNumber(0, 0)
	}
	var lim state.Amount
	if bytes.Compare(account[:], issuer[:]) < 0 {
		lim = rs.LowLimit
	} else {
		lim = rs.HighLimit
	}
	n, nerr := vaultNumber(lim.Value())
	if nerr != nil {
		return state.NewXRPLNumber(0, 0)
	}
	return n
}

// assetMatches reports whether amount denominates the vault's asset.
func assetMatches(amount tx.Amount, vd *vaultData) bool {
	if vd.AssetIsMPT {
		return amount.IsMPT()
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
		ar, err := readAccountRoot(view, accountID)
		if err != nil || ar == nil {
			return state.NewXRPLNumber(0, 0), err
		}
		reserve := config.AccountReserve(ar.OwnerCount)
		liquid := int64(ar.Balance) - int64(reserve)
		if liquid < 0 {
			liquid = 0
		}
		return state.NewXRPLNumber(liquid, 0), nil
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return state.NewXRPLNumber(0, 0), err
	}
	if accountID == issuerID {
		// The issuer's spendable balance is effectively unbounded.
		return state.NewXRPLNumber(9999999999999999, 80), nil
	}

	lineData, rerr := view.Read(keylet.Line(accountID, issuerID, asset.Currency))
	if rerr != nil || lineData == nil {
		return state.NewXRPLNumber(0, 0), nil
	}
	rs, perr := state.ParseRippleState(lineData)
	if perr != nil {
		return state.NewXRPLNumber(0, 0), perr
	}
	bal, berr := vaultNumber(rs.Balance.Value())
	if berr != nil {
		return state.NewXRPLNumber(0, 0), berr
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
		vaultAcct, err := readAccountRoot(ctx.View, vaultAccountID)
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

	amt := state.NewIssuedAmountFromValue(assetsN.Mantissa(), assetsN.Exponent(), orig.Currency, orig.Issuer)
	return tx.RippleCredit(ctx.View, ctx.AccountID, vaultAccountID, amt)
}

// sendAssetFromVault transfers assetsN of the vault asset from the vault
// pseudo-account to dstID.
func sendAssetFromVault(ctx *tx.ApplyContext, vaultAccountID, dstID [20]byte, asset tx.Asset, assetsN state.XRPLNumber) ter.Result {
	if isNativeAsset(asset) {
		drops := uint64(assetsN.ToInt64WithMode(state.RoundTowardsZero))
		vaultAcct, err := readAccountRoot(ctx.View, vaultAccountID)
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
			dst, derr := readAccountRoot(ctx.View, dstID)
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

	amt := state.NewIssuedAmountFromValue(assetsN.Mantissa(), assetsN.Exponent(), asset.Currency, asset.Issuer)
	return tx.RippleCredit(ctx.View, vaultAccountID, dstID, amt)
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
