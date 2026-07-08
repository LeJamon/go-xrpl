package lending

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

// assetIntegral reports whether asset counts indivisible units (native XRP or an
// MPT) rather than a divisible IOU.
func assetIntegral(asset tx.Asset) bool {
	return asset.IsNative() || asset.IsMPT()
}

// mathAsset projects a tx.Asset onto the minimal view the amortization math needs.
func mathAsset(asset tx.Asset) lmath.Asset {
	return lmath.Asset{Integral: assetIntegral(asset)}
}

// amountToLendNum converts a tx.Amount into a large-scale Number: drops for XRP,
// integer units for MPT, the decimal value for an IOU.
func amountToLendNum(a tx.Amount) lmath.N {
	if a.IsNative() {
		return lmath.FromDrops(a.Drops())
	}
	if a.IsMPT() {
		if raw, ok := a.MPTRaw(); ok {
			return lmath.FromDrops(raw)
		}
		return lmath.Zero()
	}
	return lendNum(a.Value())
}

// associateNum applies the sMD_NeedsAsset rounding to one NUMBER field's string
// form: it rounds the value to the asset's precision and drops a soeDEFAULT field
// that rounds to its (zero) default (rippled associateAsset, #6259).
func associateNum(s string, integral, soeDefault bool) string {
	if s == "" {
		return ""
	}
	rounded, remove := state.AssociateAssetField(lendNum(s), integral, soeDefault)
	if remove {
		return ""
	}
	return numStr(rounded)
}

// associateBrokerAsset rounds the LoanBroker's NUMBER fields to the vault asset's
// precision at the end of doApply (all three are soeDEFAULT).
func associateBrokerAsset(b *loanBrokerData, integral bool) {
	b.DebtTotal = associateNum(b.DebtTotal, integral, true)
	b.DebtMaximum = associateNum(b.DebtMaximum, integral, true)
	b.CoverAvailable = associateNum(b.CoverAvailable, integral, true)
}

// associateVaultAsset rounds the Vault's NUMBER accounting fields to the asset's
// precision. AssetsTotal, AssetsAvailable, and LossUnrealized are soeDEFAULT;
// AssetsMaximum is left untouched (LoanManage never changes it).
func associateVaultAsset(v *vault.VaultLending, integral bool) {
	v.AssetsTotal = associateNum(v.AssetsTotal, integral, true)
	v.AssetsAvailable = associateNum(v.AssetsAvailable, integral, true)
	v.LossUnrealized = associateNum(v.LossUnrealized, integral, true)
}

// associateLoanAsset rounds the Loan's NUMBER fields to the vault asset's
// precision at the end of doApply. PeriodicPayment is soeREQUIRED; the rest are
// soeDEFAULT.
func associateLoanAsset(l *loanData, integral bool) {
	l.LoanOriginationFee = associateNum(l.LoanOriginationFee, integral, true)
	l.LoanServiceFee = associateNum(l.LoanServiceFee, integral, true)
	l.LatePaymentFee = associateNum(l.LatePaymentFee, integral, true)
	l.ClosePaymentFee = associateNum(l.ClosePaymentFee, integral, true)
	l.PeriodicPayment = associateNum(l.PeriodicPayment, integral, false)
	l.PrincipalOutstanding = associateNum(l.PrincipalOutstanding, integral, true)
	l.TotalValueOutstanding = associateNum(l.TotalValueOutstanding, integral, true)
	l.ManagementFeeOutstanding = associateNum(l.ManagementFeeOutstanding, integral, true)
}
