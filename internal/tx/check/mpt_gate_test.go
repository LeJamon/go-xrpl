package check

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const mptGateID = "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"

func mptGateAmount(v int64) tx.Amount {
	return state.NewMPTAmountWithIssuanceID(v, "rIssuer", mptGateID)
}

func assertTemDisabled(t *testing.T, err error) {
	t.Helper()
	if re, ok := ter.AsResultError(err); !ok || re.Code != ter.TemDISABLED {
		t.Fatalf("want temDISABLED, got %v", err)
	}
}

func assertCheckResultError(t *testing.T, err error, want ter.Result) {
	t.Helper()
	if result, ok := ter.AsResultError(err); !ok || result.Code != want {
		t.Fatalf("want %v, got %v", want, err)
	}
}

// TestCheckMPTGate covers the MPTokensV2 checkExtraFeatures gates on CheckCreate
// (SendMax) and CheckCash (Amount / DeliverMin): an MPT amount is temDISABLED
// without the amendment and accepted with it.
func TestCheckMPTGate(t *testing.T) {
	off := amendment.NewRules(nil)
	on := amendment.NewRules([][32]byte{amendment.FeatureMPTokensV2})

	t.Run("CheckCreate MPT SendMax", func(t *testing.T) {
		c := &CheckCreate{SendMax: mptGateAmount(100)}
		assertTemDisabled(t, c.CheckExtraFeatures(off))
		if err := c.CheckExtraFeatures(on); err != nil {
			t.Fatalf("with MPTokensV2: %v", err)
		}
	})

	t.Run("CheckCreate XRP SendMax passes", func(t *testing.T) {
		c := &CheckCreate{SendMax: tx.NewXRPAmount(1000000)}
		if err := c.CheckExtraFeatures(off); err != nil {
			t.Fatalf("XRP SendMax without amendment: %v", err)
		}
	})

	t.Run("CheckCash MPT Amount", func(t *testing.T) {
		amt := mptGateAmount(100)
		c := &CheckCash{Amount: &amt}
		assertTemDisabled(t, c.CheckExtraFeatures(off))
		if err := c.CheckExtraFeatures(on); err != nil {
			t.Fatalf("with MPTokensV2: %v", err)
		}
	})

	t.Run("CheckCash MPT DeliverMin", func(t *testing.T) {
		dm := mptGateAmount(100)
		c := &CheckCash{DeliverMin: &dm}
		assertTemDisabled(t, c.CheckExtraFeatures(off))
	})
}

func TestCheckRejectsMPTWithZeroIssuer(t *testing.T) {
	source, _ := state.EncodeAccountID(checkMPTAccountID(0x51))
	destination, _ := state.EncodeAccountID(checkMPTAccountID(0x52))
	amount := state.NewMPTAmountWithIssuanceID(
		1,
		"",
		"00000001"+strings.Repeat("00", 20),
	)

	assertCheckResultError(
		t,
		NewCheckCreate(source, destination, amount).Validate(),
		ter.TemBAD_CURRENCY,
	)

	for _, useDeliverMin := range []bool{false, true} {
		cash := NewCheckCash(destination, strings.Repeat("0", 64))
		if useDeliverMin {
			cash.DeliverMin = &amount
		} else {
			cash.Amount = &amount
		}
		assertCheckResultError(t, cash.Validate(), ter.TemBAD_CURRENCY)
	}
}
