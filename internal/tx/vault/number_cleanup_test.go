package vault

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// TestRoundToVaultScale pins the fixCleanup3_2_0 deposit quantization: an IOU
// deposit is rounded down to the vault's post-deposit AssetsTotal scale, so a
// sub-ULP tail cannot be absorbed by one accounting rail and not the other.
// Integral assets are whole units and pass through untouched.
func TestRoundToVaultScale(t *testing.T) {
	// Integral: any amount is returned unchanged.
	amt := state.NewXRPLNumber(123456789, 0)
	if got := roundToVaultScale(amt, state.NewXRPLNumber(1000, 0), true); !got.Equal(amt) {
		t.Fatalf("integral roundToVaultScale changed the amount: %s", numberToString(got))
	}

	// IOU with a coarse vault total: a deposit carrying digits below the vault
	// scale is truncated down; a clean amount already at scale is unchanged.
	total := state.NewXRPLNumber(1000000, 0) // 1e6, scale -9
	clean := state.NewXRPLNumber(25, -1)     // 2.5, representable at scale -9
	if got := roundToVaultScale(clean, total, false); !got.Equal(clean) {
		t.Fatalf("clean IOU deposit changed: got %s want %s", numberToString(got), numberToString(clean))
	}

	// A dust amount far below the vault scale rounds to zero.
	dust := state.NewXRPLNumber(5, -12)
	if got := roundToVaultScale(dust, total, false); !got.IsZero() {
		t.Fatalf("sub-scale dust did not round to zero: %s", numberToString(got))
	}
}

func TestClampToAssetsTotalScale(t *testing.T) {
	total := state.NewXRPLNumber(1_000_000, 0)
	delta := state.NewXRPLNumber(15, -10)
	want := state.NewXRPLNumber(1, -9)

	if got, result := clampToAssetsTotalScale(total, delta, false); result != ter.TesSUCCESS || !got.Equal(want) {
		t.Fatalf("deposit clamp = (%s, %v), want (%s, tesSUCCESS)", got, result, want)
	}
	if got, result := clampToAssetsTotalScale(total, delta.Negate(), false); result != ter.TesSUCCESS || !got.Equal(delta) {
		t.Fatalf("withdrawal clamp = (%s, %v), want (%s, tesSUCCESS)", got, result, delta)
	}
	if _, result := clampToAssetsTotalScale(total, state.NewXRPLNumber(1, -10), false); result != ter.TecPRECISION_LOSS {
		t.Fatalf("sub-ULP clamp = %v, want tecPRECISION_LOSS", result)
	}
	if got, result := clampToAssetsTotalScale(state.NewXRPLNumber(100, 0), state.NewXRPLNumber(-3, 0), true); result != ter.TesSUCCESS || !got.Equal(state.NewXRPLNumber(3, 0)) {
		t.Fatalf("integral clamp = (%s, %v), want (3, tesSUCCESS)", got, result)
	}
}

func TestClampToAssetsTotalScaleBoundaryGrid(t *testing.T) {
	cases := []struct {
		name, total, delta, want string
	}{
		{name: "debit crosses decade", total: "1000000", delta: "-73e-11", want: "7e-10"},
		{name: "debit finer scale", total: "1", delta: "-73e-17", want: "7e-16"},
		{name: "credit crosses scale", total: "9999999999999999e-15", delta: "5", want: "4999999999999991e-15"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			total, err := vaultNumber(test.total)
			if err != nil {
				t.Fatal(err)
			}
			delta, err := vaultNumber(test.delta)
			if err != nil {
				t.Fatal(err)
			}
			want, err := vaultNumber(test.want)
			if err != nil {
				t.Fatal(err)
			}
			got, result := clampToAssetsTotalScale(total, delta, false)
			if result != ter.TesSUCCESS || got.Cmp(want) != 0 {
				t.Fatalf("clamp = (%s, %v), want (%s, tesSUCCESS)", got, result, want)
			}
		})
	}
	if _, result := clampToAssetsTotalScale(
		state.NewXRPLNumber(1_000_000, 0),
		state.NewXRPLNumber(9_999_999_999_999_999, -25),
		false,
	); result != ter.TecPRECISION_LOSS {
		t.Fatalf("credit rounding leak = %v, want tecPRECISION_LOSS", result)
	}
	if _, result := clampToAssetsTotalScale(
		state.NewXRPLNumber(1_000_000, 0),
		state.NewXRPLNumber(4, -10),
		false,
	); result != ter.TecPRECISION_LOSS {
		t.Fatalf("sub-ULP credit = %v, want tecPRECISION_LOSS", result)
	}
}

func TestDebitIsNonZeroDust(t *testing.T) {
	total := state.NewXRPLNumber(1_000_000, 0)
	if !debitIsNonZeroDust(total, state.NewXRPLNumber(1, -10), false) {
		t.Fatal("sub-ULP IOU debit should be dust")
	}
	if debitIsNonZeroDust(total, state.NewXRPLNumber(1, -9), false) {
		t.Fatal("one-ULP IOU debit should change the stored total")
	}
	if debitIsNonZeroDust(total, state.NewXRPLNumber(0, 0), false) {
		t.Fatal("zero debit should not be dust")
	}
}

func TestAssetsToSharesWithdrawLossExhaustion(t *testing.T) {
	shareTotal := state.NewXRPLNumber(100, 0)
	if got := assetsToSharesWithdraw(
		state.NewXRPLNumber(100, 0),
		state.NewXRPLNumber(100, 0),
		shareTotal,
		state.NewXRPLNumber(1, 0),
		true,
	); !got.Equal(shareTotal) {
		t.Fatalf("loss-exhausted withdrawal shares = %s, want %s", got, shareTotal)
	}
}

func TestAssetWithdrawalAmountsTruncatesShares(t *testing.T) {
	total := state.NewXRPLNumber(3, 0)
	shares := state.NewXRPLNumber(2, 0)
	assets := state.NewXRPLNumber(1, 0)
	nearest, _ := assetWithdrawalAmountsWithMode(total, state.NewXRPLNumber(0, 0), shares, assets, true, false)
	truncated, payout := assetWithdrawalAmountsWithMode(total, state.NewXRPLNumber(0, 0), shares, assets, true, true)
	if got := nearest.ToInt64WithMode(state.RoundTowardsZero); got != 1 {
		t.Fatalf("nearest shares = %d, want 1", got)
	}
	if !truncated.IsZero() || !payout.IsZero() {
		t.Fatalf("truncated shares/payout = (%s, %s), want (0, 0)", truncated, payout)
	}
}

func TestAssetWithdrawalAmountsNeverOverpays(t *testing.T) {
	cases := []struct {
		name, total, shares, assets string
	}{
		{name: "fractional", total: "66665e-1", shares: "5e9", assets: "1000"},
		{name: "precision boundary", total: "9999999999999999", shares: "1000000000000000", assets: "2"},
		{name: "tiny", total: "1000000", shares: "2e15", assets: "16e-10"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			total, err := vaultNumber(test.total)
			if err != nil {
				t.Fatal(err)
			}
			shareTotal, err := vaultNumber(test.shares)
			if err != nil {
				t.Fatal(err)
			}
			assets, err := vaultNumber(test.assets)
			if err != nil {
				t.Fatal(err)
			}
			_, payout := assetWithdrawalAmountsWithMode(total, state.NewXRPLNumber(0, 0), shareTotal, assets, false, true)
			if payout.Cmp(assets) > 0 {
				t.Fatalf("payout %s exceeds requested %s", payout, assets)
			}
		})
	}
}
