package vault

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// readAccountRoot reads and parses an AccountRoot, returning nil when absent.
func readAccountRoot(view tx.LedgerView, id [20]byte) (*state.AccountRoot, error) {
	data, err := view.Read(keylet.Account(id))
	if err != nil || data == nil {
		return nil, err
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
