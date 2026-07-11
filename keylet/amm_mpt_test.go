package keylet

import "testing"

func TestAMMAssetIssueCompatibility(t *testing.T) {
	cur1 := [20]byte{1}
	iss1 := [20]byte{2}
	cur2 := [20]byte{3}
	iss2 := [20]byte{4}

	want := indexHash(spaceAMM, iss1[:], cur1[:], iss2[:], cur2[:])
	legacy := AMM(iss1, cur1, iss2, cur2)
	asset := AMMAsset(IssueSide(cur1, iss1), IssueSide(cur2, iss2))
	if legacy.Key != want || asset.Key != want {
		t.Fatalf("Issue/Issue AMM key changed: legacy=%x asset=%x want=%x", legacy.Key, asset.Key, want)
	}
}

func TestAMMAssetMPTLayouts(t *testing.T) {
	cur := [20]byte{3}
	iss := [20]byte{4}
	mpt1 := [24]byte{1}
	mpt2 := [24]byte{2}

	tests := []struct {
		name   string
		asset1 BookSide
		asset2 BookSide
		want   [32]byte
	}{
		{
			name:   "Issue MPT",
			asset1: IssueSide(cur, iss),
			asset2: MPTSide(mpt1),
			want:   indexHash(spaceAMM, mpt1[:], iss[:], cur[:]),
		},
		{
			name:   "MPT Issue reverse input",
			asset1: MPTSide(mpt1),
			asset2: IssueSide(cur, iss),
			want:   indexHash(spaceAMM, mpt1[:], iss[:], cur[:]),
		},
		{
			name:   "MPT MPT",
			asset1: MPTSide(mpt2),
			asset2: MPTSide(mpt1),
			want:   indexHash(spaceAMM, mpt1[:], mpt2[:]),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := AMMAsset(test.asset1, test.asset2)
			if got.Key != test.want {
				t.Fatalf("AMMAsset key=%x, want %x", got.Key, test.want)
			}
		})
	}
}
