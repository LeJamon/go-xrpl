package payment

import (
	"encoding/hex"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// CheckDelegatePermission authorizes a delegated Payment carrying a granular
// PaymentMint or PaymentBurn permission. A transaction-level Payment grant is
// already resolved by the engine before this runs.
//
// Mint: the sender (sfAccount) issues the delivered currency. Burn: the
// destination issues it. XRP is never a mint or burn. Granular permissions are
// only valid for direct payments, so a cross-currency SendMax or any Paths
// forbids the grant; the mint/burn checks cover both IOU and MPT.
func (p *Payment) CheckDelegatePermission(pc tx.DelegatePermissionContext) ter.Result {
	account := p.GetCommon().Account
	amt := p.Amount

	if (p.SendMax != nil && !sameAsset(*p.SendMax, amt)) || len(p.Paths) > 0 {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	return paymentMintBurn(pc, amt, account, p.Destination)
}

func paymentMintBurn(pc tx.DelegatePermissionContext, amt state.Amount, account, destination string) ter.Result {
	if amt.Native {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	issuer := amountIssuer(amt)
	if pc.HasGranular(tx.GranularPaymentMint) && issuer == account {
		return ter.TesSUCCESS
	}
	if pc.HasGranular(tx.GranularPaymentBurn) && issuer == destination {
		return ter.TesSUCCESS
	}
	return ter.TerNO_DELEGATE_PERMISSION
}

// amountIssuer returns the r-address of an asset's issuer: the currency issuer
// for an IOU, or the account encoded in the issuance ID for an MPT.
func amountIssuer(amt state.Amount) string {
	if !amt.IsMPT() {
		return amt.Issuer
	}
	id := amt.MPTIssuanceID()
	if len(id) != 48 {
		return ""
	}
	issuerBytes, err := hex.DecodeString(id[8:])
	if err != nil {
		return ""
	}
	addr, err := addresscodec.EncodeAccountIDToClassicAddress(issuerBytes)
	if err != nil {
		return ""
	}
	return addr
}

// sameAsset reports whether two amounts denote the same asset: both XRP, the
// same IOU currency+issuer, or the same MPT issuance.
func sameAsset(a, b state.Amount) bool {
	if a.Native != b.Native {
		return false
	}
	if a.Native {
		return true
	}
	if a.IsMPT() || b.IsMPT() {
		return a.IsMPT() && b.IsMPT() && a.MPTIssuanceID() == b.MPTIssuanceID()
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}
