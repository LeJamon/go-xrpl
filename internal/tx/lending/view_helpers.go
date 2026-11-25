package lending

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
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

func amountToLendNumForRules(a tx.Amount, rules *amendment.Rules) lmath.N {
	scale := lendingNumberScale(rules)
	if a.IsNative() {
		return lmath.FromDropsScaled(a.Drops(), scale)
	}
	if a.IsMPT() {
		if raw, ok := a.MPTRaw(); ok {
			return lmath.FromDropsScaled(raw, scale)
		}
		return lmath.NumScaled(0, 0, scale)
	}
	return lendNumForRules(a.Value(), rules)
}

// associateNum applies the sMD_NeedsAsset rounding to one NUMBER field's string
// form: it rounds the value to the asset's precision and drops a soeDEFAULT field
// that rounds to its (zero) default (rippled associateAsset, #6259).
func associateNum(s string, integral, soeDefault bool, rules *amendment.Rules) string {
	if s == "" {
		return ""
	}
	rounded, remove := state.AssociateAssetField(lendNumForRules(s, rules), integral, soeDefault)
	if remove {
		return ""
	}
	return numStr(rounded)
}

// associateBrokerAsset rounds the LoanBroker's NUMBER fields to the vault asset's
// precision at the end of doApply (all three are soeDEFAULT).
func associateBrokerAsset(b *loanBrokerData, integral bool, rules *amendment.Rules) {
	b.DebtTotal = associateNum(b.DebtTotal, integral, true, rules)
	b.DebtMaximum = associateNum(b.DebtMaximum, integral, true, rules)
	b.CoverAvailable = associateNum(b.CoverAvailable, integral, true, rules)
}

func associateVaultAsset(v *vault.VaultLending, rules *amendment.Rules) ter.Result {
	native := v.Asset.IsNative()
	integral := native || v.Asset.IsMPT()
	for _, field := range []*string{&v.AssetsTotal, &v.AssetsAvailable, &v.AssetsMaximum, &v.LossUnrealized} {
		rounded := lendNumForRules(*field, rules).RoundToAsset(integral)
		if native && exceedsXRPRange(rounded) {
			return ter.TefEXCEPTION
		}
		*field = numStr(rounded)
	}
	return ter.TesSUCCESS
}

func associateLoanAsset(l *loanData, integral bool, rules *amendment.Rules) {
	l.PrincipalOutstanding = associateNum(l.PrincipalOutstanding, integral, true, rules)
	l.TotalValueOutstanding = associateNum(l.TotalValueOutstanding, integral, true, rules)
	l.ManagementFeeOutstanding = associateNum(l.ManagementFeeOutstanding, integral, true, rules)
}
