package engine

import (
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

// TestToInvariantsAsset_PreservesMPTIssuanceID guards the fix for the adapter
// silently dropping MPTIssuanceID when converting tx.Asset to invariants.Asset —
// an MPT-asset AMM invariant needs the issuance ID to locate the pool holding.
func TestToInvariantsAsset_PreservesMPTIssuanceID(t *testing.T) {
	const mptID = "00000539C35BFC42B69F7A19B7C4C5B5D5E7F9A1B3C5D7E9"

	mpt := toInvariantsAsset(txcore.Asset{MPTIssuanceID: mptID})
	if mpt.MPTIssuanceID != mptID {
		t.Fatalf("MPTIssuanceID dropped: got %q want %q", mpt.MPTIssuanceID, mptID)
	}
	if !mpt.IsMPT() {
		t.Fatalf("converted MPT asset should report IsMPT()")
	}

	iou := toInvariantsAsset(txcore.Asset{Currency: "USD", Issuer: "rIssuer"})
	if iou.Currency != "USD" || iou.Issuer != "rIssuer" || iou.MPTIssuanceID != "" {
		t.Fatalf("IOU conversion wrong: %+v", iou)
	}

	xrp := toInvariantsAsset(txcore.Asset{Currency: "XRP"})
	if !xrp.IsNative() || xrp.IsMPT() {
		t.Fatalf("XRP conversion wrong: %+v", xrp)
	}
}
