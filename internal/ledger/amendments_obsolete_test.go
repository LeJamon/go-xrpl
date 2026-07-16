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

// NFToken transactions gate on the obsolete NonFungibleTokensV1 (and on
// fixNFTokenNegOffer / fixNFTokenDirV1). Those three IDs are never written to a
// ledger's Amendments object — only their subsuming amendment, NonFungibleTokensV1_1,
// is. The runtime rules must report the three enabled whenever V1_1 is enabled,
// mirroring rippled's injection in Rules::enabled. Otherwise a node temDISABLEs
// NFToken transactions that rippled applies, forking the ledger.
func TestLoadAmendments_NFTokenV1InjectedFromV1_1(t *testing.T) {
	// An Amendments object enabling AMM and NonFungibleTokensV1_1 (but NOT the
	// obsolete V1 / fix* IDs, which rippled never stores).
	data := encodeAmendmentsEntry(t, [][32]byte{
		amendment.FeatureAMM,
		amendment.FeatureNonFungibleTokensV1_1,
	})

	rules, err := LoadAmendmentsFromLedgerEntry(data)
	if err != nil {
		t.Fatalf("LoadAmendmentsFromLedgerEntry: %v", err)
	}

	if !rules.Enabled(amendment.FeatureAMM) {
		t.Error("AMM must be enabled (present in the Amendments object)")
	}
	if !rules.Enabled(amendment.FeatureNonFungibleTokensV1_1) {
		t.Error("NonFungibleTokensV1_1 must be enabled (present in the Amendments object)")
	}
	for _, f := range []struct {
		name string
		id   [32]byte
	}{
		{"NonFungibleTokensV1", amendment.FeatureNonFungibleTokensV1},
		{"fixNFTokenNegOffer", amendment.FeatureFixNFTokenNegOffer},
		{"fixNFTokenDirV1", amendment.FeatureFixNFTokenDirV1},
	} {
		if !rules.Enabled(f.id) {
			t.Errorf("%s must be reported enabled via the V1_1 injection, even though its own ID is absent from the Amendments object", f.name)
		}
	}
}

// Retired amendments (pre-amendment code gate removed in rippled, so the code
// runs unconditionally) are permanently enabled even when absent from the
// Amendments object. goXRPL still gates on them (e.g. PaymentChannel* require
// FeaturePayChan), so the runtime rules must report them enabled. Since
// NonFungibleTokensV1_1 is now retired (permanently enabled), the obsolete
// NFToken amendments it subsumes (NonFungibleTokensV1, fixNFTokenNegOffer,
// fixNFTokenDirV1) are always reported enabled via the injection.
func TestLoadAmendments_RetiredPermanentlyEnabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  [][32]byte
	}{
		{"empty Amendments object", nil},
		{"unrelated amendment only", [][32]byte{amendment.FeatureAMM}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := encodeAmendmentsEntry(t, tc.ids)
			rules, err := LoadAmendmentsFromLedgerEntry(data)
			if err != nil {
				t.Fatalf("LoadAmendmentsFromLedgerEntry: %v", err)
			}
			if !rules.Enabled(amendment.FeaturePayChan) {
				t.Error("retired PayChan must be permanently enabled even when absent from the Amendments object")
			}
			// NonFungibleTokensV1_1 is retired, so its subsumed obsolete IDs
			// report enabled via the injection even when absent from the SLE.
			if !rules.Enabled(amendment.FeatureNonFungibleTokensV1) {
				t.Error("NonFungibleTokensV1 must report enabled via the retired V1_1 injection")
			}
			// A genuinely unsupported amendment must NOT be enabled by the baseline.
			if rules.Enabled(amendment.FeatureSingleAssetVault) {
				t.Error("SingleAssetVault (SupportedNo) must NOT be enabled by the permanent baseline")
			}
		})
	}
}

func TestIsAmendmentStoredInLedgerEntry_ExactMembership(t *testing.T) {
	data := encodeAmendmentsEntry(t, [][32]byte{
		amendment.FeatureFlow,
		amendment.FeatureNonFungibleTokensV1_1,
	})

	for _, tc := range []struct {
		name string
		id   [32]byte
		want bool
	}{
		{name: "stored", id: amendment.FeatureFlow, want: true},
		{name: "retired baseline is not injected", id: amendment.FeatureFixPayChanRecipientOwnerDir},
		{name: "rule alias is not treated as stored", id: amendment.FeatureNonFungibleTokensV1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsAmendmentStoredInLedgerEntry(data, tc.id)
			if err != nil {
				t.Fatalf("IsAmendmentStoredInLedgerEntry: %v", err)
			}
			if got != tc.want {
				t.Fatalf("IsAmendmentStoredInLedgerEntry = %t, want %t", got, tc.want)
			}
		})
	}

	current, err := LoadAmendmentsFromLedgerEntry(data)
	if err != nil {
		t.Fatalf("LoadAmendmentsFromLedgerEntry: %v", err)
	}
	if !current.Enabled(amendment.FeatureFixPayChanRecipientOwnerDir) {
		t.Fatal("the default loader must preserve the current retired-amendment baseline")
	}
}
