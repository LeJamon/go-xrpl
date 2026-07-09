package lending

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// brokerCoverRate is the raw cover-rate product: the cover-rate minimum applied
// to the broker's debt total, rounded up with no asset-scale rounding.
func brokerCoverRate(debtTotal lmath.N, coverRateMinimum uint32) lmath.N {
	return lmath.TenthBipsOfValueRounded(debtTotal, coverRateMinimum, state.RoundUpward)
}

// brokerCoverRateAtScale rounds the cover-rate product up to a decimal scale.
func brokerCoverRateAtScale(debtTotal lmath.N, coverRateMinimum uint32, scale int, integral bool) lmath.N {
	return lmath.RoundAssetUpward(lmath.Asset{Integral: integral}, brokerCoverRate(debtTotal, coverRateMinimum), scale)
}

// minimumBrokerCover is the post-fixCleanup3_2_0 minimum first-loss cover: the
// cover-rate product rounded up at the vault's AssetsTotal scale. DebtTotal is a
// broker-level aggregate kept at vault scale, so the rounding must use the vault
// scale — never an individual loan's scale.
func minimumBrokerCover(debtTotal lmath.N, coverRateMinimum uint32, vaultScale int, integral bool) lmath.N {
	return brokerCoverRateAtScale(debtTotal, coverRateMinimum, vaultScale, integral)
}

// canApplyToBrokerCover rejects a cover deposit/withdraw/clawback amount so small
// it rounds to zero at the broker's CoverAvailable scale (fixCleanup3_2_0),
// preventing a silent sub-ULP no-op where both the pseudo holding and
// CoverAvailable absorb the same rounded zero. Ungated it always succeeds.
func canApplyToBrokerCover(fix320 bool, coverAvailable, amount lmath.N, integral bool) ter.Result {
	if !fix320 {
		return ter.TesSUCCESS
	}
	if amount.IsZero() {
		return ter.TecPRECISION_LOSS
	}
	coverScale := coverAvailable.AssetExponent(integral, state.RoundToNearest)
	if amount.RoundToAssetScale(integral, coverScale, state.RoundToNearest).IsZero() {
		return ter.TecPRECISION_LOSS
	}
	return ter.TesSUCCESS
}

// roundCoverDeposit quantizes a cover-deposit amount down to the broker's
// CoverAvailable scale (fixCleanup3_2_0) so the same value drives both the
// trustline transfer and the CoverAvailable increment; sub-scale dust is
// rejected with tecPRECISION_LOSS. Ungated it returns the amount unchanged.
func roundCoverDeposit(fix320 bool, coverAvailable, amount lmath.N, integral bool) (lmath.N, ter.Result) {
	if !fix320 {
		return amount, ter.TesSUCCESS
	}
	coverScale := coverAvailable.AssetExponent(integral, state.RoundToNearest)
	rounded := amount.RoundToAssetScale(integral, coverScale, state.RoundDownward)
	if rounded.IsZero() {
		return lmath.Zero(), ter.TecPRECISION_LOSS
	}
	return rounded, ter.TesSUCCESS
}
