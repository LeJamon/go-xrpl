package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const mptGateID = "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"

func mptGateAsset() tx.Asset { return tx.Asset{MPTIssuanceID: mptGateID} }
func iouGateAsset() tx.Asset { return tx.Asset{Currency: "USD", Issuer: "rGateway"} }
func mptGateAmount() tx.Amount {
	return state.NewMPTAmountWithIssuanceID(100, "rIssuer", mptGateID)
}

type extraFeaturer interface {
	CheckExtraFeatures(*amendment.Rules) error
}

// TestAMMMPTGate covers the MPTokensV2 checkExtraFeatures gate on every AMM
// transactor: an MPT asset or amount is temDISABLED without the amendment.
func TestAMMMPTGate(t *testing.T) {
	off := amendment.NewRules(nil)
	on := amendment.NewRules([][32]byte{amendment.FeatureMPTokensV2})

	iou := iouGateAsset()
	mpt := mptGateAsset()
	mAmt := mptGateAmount()

	cases := []struct {
		name string
		tx   extraFeaturer
	}{
		{"AMMCreate MPT Amount", &AMMCreate{Amount: mptGateAmount(), Amount2: tx.NewXRPAmount(1)}},
		{"AMMDeposit MPT Asset", &AMMDeposit{Asset: mpt, Asset2: iou}},
		{"AMMDeposit MPT Amount", &AMMDeposit{Asset: iou, Asset2: iou, Amount: &mAmt}},
		{"AMMWithdraw MPT Asset2", &AMMWithdraw{Asset: iou, Asset2: mpt}},
		{"AMMBid MPT Asset", &AMMBid{Asset: mpt, Asset2: iou}},
		{"AMMVote MPT Asset", &AMMVote{Asset: mpt, Asset2: iou}},
		{"AMMDelete MPT Asset2", &AMMDelete{Asset: iou, Asset2: mpt}},
		{"AMMClawback MPT Amount", &AMMClawback{Asset: iou, Asset2: iou, Amount: &mAmt}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tx.CheckExtraFeatures(off)
			if re, ok := ter.AsResultError(err); !ok || re.Code != ter.TemDISABLED {
				t.Fatalf("without MPTokensV2: want temDISABLED, got %v", err)
			}
			if err := tc.tx.CheckExtraFeatures(on); err != nil {
				t.Fatalf("with MPTokensV2: %v", err)
			}
		})
	}

	// An all-IOU AMM tx passes the gate regardless of the amendment.
	if err := (&AMMVote{Asset: iou, Asset2: iouGateAsset()}).CheckExtraFeatures(off); err != nil {
		t.Fatalf("IOU AMMVote without amendment: %v", err)
	}
}
