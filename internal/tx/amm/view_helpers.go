package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// noDefaultRipple reports whether the asset's issuer lacks lsfDefaultRipple,
// which is a problem for AMM. It returns false for XRP, a missing/unparseable
// issuer, or when DefaultRipple is set.
// Reference: rippled AMMCreate.cpp lines 126-135
func noDefaultRipple(view tx.LedgerView, asset tx.Asset) bool {
	if isXRPAsset(asset) {
		return false
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return false
	}

	issuerData, err := view.Read(keylet.Account(issuerID))
	if err != nil || issuerData == nil {
		return false
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return false
	}

	return (issuerAccount.Flags & state.LsfDefaultRipple) == 0
}

// insufficientBalance reports whether the account cannot fund the amount. For
// XRP it compares against the liquid balance; for IOU it compares held funds
// (issuers have unlimited supply).
// Reference: rippled AMMCreate.cpp line 153-163
func insufficientBalance(view tx.LedgerView, accountID [20]byte, amount tx.Amount, xrpLiquid int64) bool {
	if amount.IsNative() {
		return xrpLiquid < amount.Drops()
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return true
	}
	if accountID == issuerID {
		return false
	}

	held := tx.AccountFunds(view, accountID, amount, true, 0, 0)
	return held.Compare(amount) < 0
}

// isLPToken reports whether the amount is issued by an AMM pseudo-account.
// Reference: rippled AMMCreate.cpp line 172-177
func isLPToken(view tx.LedgerView, amount tx.Amount) bool {
	if amount.IsNative() {
		return false
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return false
	}

	issuerData, err := view.Read(keylet.Account(issuerID))
	if err != nil || issuerData == nil {
		return false
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return false
	}

	return issuerAccount.IsPseudoAccount()
}

// setAMMNodeFlag sets lsfAMMNode on the AMM's trust line for an IOU asset.
// Reference: rippled AMMCreate.cpp sendAndTrustSet line 297-306
func setAMMNodeFlag(ammAccountID [20]byte, asset tx.Asset, view tx.LedgerView) error {
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return err
	}

	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return err
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return err
	}

	rs.Flags |= state.LsfAMMNode

	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	return view.Update(trustLineKey, rsBytes)
}

// clawbackDisabled returns tecNO_PERMISSION when the asset's issuer has
// lsfAllowTrustLineClawback set, tecINTERNAL when the issuer cannot be read,
// and tesSUCCESS otherwise. XRP always passes.
// Reference: rippled AMMCreate.cpp preclaim lines 201-210
func clawbackDisabled(view tx.LedgerView, asset tx.Asset) tx.Result {
	if isXRPAsset(asset) {
		return tx.TesSUCCESS
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return tx.TecINTERNAL
	}

	issuerData, err := view.Read(keylet.Account(issuerID))
	if err != nil || issuerData == nil {
		return tx.TecINTERNAL
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TecINTERNAL
	}

	if (issuerAccount.Flags & state.LsfAllowTrustLineClawback) != 0 {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}
