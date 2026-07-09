package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// TestBrokerMinimumCover_AmendmentScale pins the fixCleanup3_2_0 min-cover scale
// fix: post-amendment the minimum first-loss cover is rounded at the vault's
// AssetsTotal scale, whereas the pre-amendment paths round at a finer per-loan /
// debt scale (or not at all). DebtTotal is a broker aggregate maintained at vault
// scale, so rounding at a finer scale under-computes the requirement — the fix
// makes the two eras observably diverge whenever the cover-rate product carries
// precision below the vault scale.
func TestBrokerMinimumCover_AmendmentScale(t *testing.T) {
	// debt 0.007 at 33.333% → cover-rate product 0.00233331, which has digits
	// below a vault scale of -6.
	debt := lmath.Num(7, -3)
	rate := uint32(33333)

	postVaultScale := minimumBrokerCover(debt, rate, -6, false) // fixCleanup3_2_0 path
	preDebtScale := brokerCoverRateAtScale(debt, rate, -9, false)
	preRaw := brokerCoverRate(debt, rate)

	if postVaultScale.Equal(preDebtScale) {
		t.Fatalf("post cover at vault scale (%s) must differ from pre at debt scale (%s)",
			numStr(postVaultScale), numStr(preDebtScale))
	}
	if postVaultScale.Equal(preRaw) {
		t.Fatalf("post cover at vault scale (%s) must differ from raw pre-amendment cover (%s)",
			numStr(postVaultScale), numStr(preRaw))
	}

	// Integral assets round to whole units regardless of scale, so the fix is a
	// no-op: 50% of 3 drops ceils to 2 in both eras.
	postXRP := minimumBrokerCover(lmath.FromDrops(3), 50000, 0, true)
	preXRP := brokerCoverRate(lmath.FromDrops(3), 50000)
	if !postXRP.Equal(lmath.FromDrops(2)) {
		t.Fatalf("integral post cover = %s, want 2", numStr(postXRP))
	}
	if postXRP.Equal(preXRP) {
		// preXRP is the raw 1.5; post ceils to 2. Sanity that integral post still ceils.
		t.Fatalf("integral post cover should ceil, got raw %s", numStr(preXRP))
	}
}

func TestCanApplyToBrokerCover(t *testing.T) {
	cover := lmath.Num(1000000, 0) // IOU, AssetsTotal scale -9
	dust := lmath.Num(5, -12)      // below cover scale
	whole := lmath.Num(1, 0)

	cases := []struct {
		name     string
		fix320   bool
		amount   lmath.N
		integral bool
		want     ter.Result
	}{
		{"disabled tolerates dust", false, dust, false, ter.TesSUCCESS},
		{"disabled tolerates zero", false, lmath.Zero(), false, ter.TesSUCCESS},
		{"enabled rejects zero", true, lmath.Zero(), false, ter.TecPRECISION_LOSS},
		{"enabled rejects sub-scale dust", true, dust, false, ter.TecPRECISION_LOSS},
		{"enabled accepts representable", true, whole, false, ter.TesSUCCESS},
		{"enabled rejects zero xrp", true, lmath.Zero(), true, ter.TecPRECISION_LOSS},
		{"enabled accepts drops", true, lmath.FromDrops(5), true, ter.TesSUCCESS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canApplyToBrokerCover(tc.fix320, cover, tc.amount, tc.integral); got != tc.want {
				t.Fatalf("canApplyToBrokerCover = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoundCoverDeposit(t *testing.T) {
	cover := lmath.Num(1000000, 0) // IOU, scale -9
	dust := lmath.Num(5, -12)
	amt := lmath.Num(25, -1) // 2.5, representable at scale -9

	// Disabled: amount passes through untouched.
	if got, res := roundCoverDeposit(false, cover, dust, false); res != ter.TesSUCCESS || !got.Equal(dust) {
		t.Fatalf("disabled roundCoverDeposit = (%s,%v), want (%s,tes)", numStr(got), res, numStr(dust))
	}
	// Enabled: sub-scale dust rounds to zero → precision loss.
	if _, res := roundCoverDeposit(true, cover, dust, false); res != ter.TecPRECISION_LOSS {
		t.Fatalf("enabled dust = %v, want tecPRECISION_LOSS", res)
	}
	// Enabled: representable amount is quantized down and preserved.
	if got, res := roundCoverDeposit(true, cover, amt, false); res != ter.TesSUCCESS || !got.Equal(amt) {
		t.Fatalf("enabled roundCoverDeposit = (%s,%v), want (2.5,tes)", numStr(got), res)
	}
	// Integral: deposit is unchanged (whole units already representable).
	if got, res := roundCoverDeposit(true, lmath.FromDrops(1000), lmath.FromDrops(7), true); res != ter.TesSUCCESS || !got.Equal(lmath.FromDrops(7)) {
		t.Fatalf("integral roundCoverDeposit = (%s,%v), want (7,tes)", numStr(got), res)
	}
	_ = state.RoundToNearest
}
