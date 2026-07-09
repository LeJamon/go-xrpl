package vault

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
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
