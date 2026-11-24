package amendment

import "testing"

var nftPredecessors = map[string][32]byte{
	"NonFungibleTokensV1": FeatureNonFungibleTokensV1,
	"fixNFTokenNegOffer":  FeatureFixNFTokenNegOffer,
	"fixNFTokenDirV1":     FeatureFixNFTokenDirV1,
}

// rippled no longer registers NonFungibleTokensV1_1's predecessors, so one
// enabled on a ledger amendment-blocks it (unsupportedEnabled_).
func TestNFTPredecessorEnabledBlocks(t *testing.T) {
	for name, id := range nftPredecessors {
		t.Run(name, func(t *testing.T) {
			table := NewTable()
			table.DoValidatedLedger(256, map[[32]byte]bool{FeatureNonFungibleTokensV1_1: true}, nil)
			if table.IsBlocked() {
				t.Fatal("NonFungibleTokensV1_1 alone must not block")
			}
			table.DoValidatedLedger(512, map[[32]byte]bool{FeatureNonFungibleTokensV1_1: true, id: true}, nil)
			if !table.HasUnsupportedEnabled() || !table.IsBlocked() {
				t.Fatalf("%s enabled must amendment-block the node", name)
			}
		})
	}
}

// A majority for one of them raises the unsupported-majority warning
// (firstUnsupportedExpected_) like any amendment rippled does not know.
func TestNFTPredecessorMajorityWarns(t *testing.T) {
	const majorityAt uint32 = 800_000_000
	for name, id := range nftPredecessors {
		t.Run(name, func(t *testing.T) {
			table := NewTable()
			table.DoValidatedLedger(256, nil, map[[32]byte]uint32{id: majorityAt})
			expected, ok := table.FirstUnsupportedExpected()
			if !ok || expected != majorityAt+majorityTimeSeconds {
				t.Fatalf("%s majority: expected = %d, %t; want %d", name, expected, ok, majorityAt+majorityTimeSeconds)
			}
			if table.IsBlocked() {
				t.Fatal("a majority alone must not block")
			}
		})
	}
}

// NFT gating still derives the three rule IDs from NonFungibleTokensV1_1.
func TestNFTPredecessorRulesFollowSuccessor(t *testing.T) {
	enabled := NewRules([][32]byte{FeatureNonFungibleTokensV1_1})
	disabled := NewRules(nil)
	for name, id := range nftPredecessors {
		if !enabled.Enabled(id) || disabled.Enabled(id) {
			t.Errorf("%s must follow NonFungibleTokensV1_1", name)
		}
		if f := FeatureByID(id); f == nil || f.IsSupported() || f.Retired {
			t.Errorf("%s must stay registered as unsupported", name)
		}
	}
	if !AllSupportedRules().Enabled(FeatureFixNFTokenDirV1) {
		t.Error("supported rules enable NonFungibleTokensV1_1 and so its predecessors")
	}
}
