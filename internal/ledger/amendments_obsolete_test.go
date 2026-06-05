package ledger

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func encodeAmendmentsEntry(t *testing.T, ids [][32]byte) []byte {
	t.Helper()
	hexes := make([]string, len(ids))
	for i, id := range ids {
		hexes[i] = fmt.Sprintf("%064X", id)
	}
	h, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType": "Amendments",
		"Flags":           uint32(0),
		"Amendments":      hexes,
	})
	if err != nil {
		t.Fatalf("encode Amendments entry: %v", err)
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return b
}

// NFToken (and a few other) transactions gate on the obsolete-but-supported
// NonFungibleTokensV1 amendment. Obsolete/retired amendments are never written
// to a ledger's Amendments object, yet they are permanently part of the
// protocol. The runtime rules must still report them enabled (mirrors rippled
// mapping VoteBehavior::Obsolete -> AmendmentSupport::Retired = always enabled).
// Otherwise a running node temDISABLEs NFToken transactions that rippled
// applies, forking the ledger.
func TestLoadAmendments_ObsoletePermanentlyEnabled(t *testing.T) {
	// An Amendments object enabling AMM but NOT NonFungibleTokensV1.
	data := encodeAmendmentsEntry(t, [][32]byte{amendment.FeatureAMM})

	rules, err := LoadAmendmentsFromLedgerEntry(data)
	if err != nil {
		t.Fatalf("LoadAmendmentsFromLedgerEntry: %v", err)
	}

	if !rules.Enabled(amendment.FeatureAMM) {
		t.Error("AMM must be enabled (it is present in the Amendments object)")
	}
	if !rules.Enabled(amendment.FeatureNonFungibleTokensV1) {
		t.Error("NonFungibleTokensV1 (obsolete, supported) must be permanently enabled even though it is absent from the Amendments object")
	}
	// Sanity: the baseline must not over-enable an unsupported amendment.
	if rules.Enabled(amendment.FeatureSingleAssetVault) {
		t.Error("SingleAssetVault (SupportedNo) must NOT be enabled by the permanent baseline")
	}
}

// Empty/missing Amendments object (very early genesis state) still has the
// permanently-enabled obsolete/retired amendments active.
func TestLoadAmendments_EmptyEntryHasPermanentBaseline(t *testing.T) {
	data := encodeAmendmentsEntry(t, nil)
	rules, err := LoadAmendmentsFromLedgerEntry(data)
	if err != nil {
		t.Fatalf("LoadAmendmentsFromLedgerEntry: %v", err)
	}
	if !rules.Enabled(amendment.FeatureNonFungibleTokensV1) {
		t.Error("NonFungibleTokensV1 must be enabled by the permanent baseline even with an empty Amendments object")
	}
}
