package state

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDirectoryNodeMPTBookRoundTrip covers the MPTokensV2 order-book directory
// fields: a book side that is an MPT carries the 192-bit MPTokenIssuanceID
// (TakerPaysMPT / TakerGetsMPT) instead of the currency/issuer pair, and the
// serializer must round-trip either shape byte-identically.
func TestDirectoryNodeMPTBookRoundTrip(t *testing.T) {
	paysMPT := [24]byte{0x00, 0x00, 0x00, 0x01, 0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF, 0x12}
	getsMPT := [24]byte{0x00, 0x00, 0x00, 0x02, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11, 0x22, 0x33, 0x44}

	cases := []struct {
		name string
		dir  *DirectoryNode
	}{
		{
			name: "MPT/MPT book",
			dir: &DirectoryNode{
				RootIndex:    [32]byte{1},
				Indexes:      [][32]byte{{2}},
				TakerPaysMPT: &paysMPT,
				TakerGetsMPT: &getsMPT,
				ExchangeRate: 0x5a00000000000000,
			},
		},
		{
			name: "MPT-pays / IOU-gets book",
			dir: &DirectoryNode{
				RootIndex:         [32]byte{1},
				Indexes:           [][32]byte{{2}},
				TakerPaysMPT:      &paysMPT,
				TakerGetsCurrency: [20]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'U', 'S', 'D', 0, 0, 0, 0, 0},
				TakerGetsIssuer:   [20]byte{9},
				ExchangeRate:      0x5a00000000000000,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := SerializeDirectoryNode(tc.dir, true)
			require.NoError(t, err)

			got, err := ParseDirectoryNode(data)
			require.NoError(t, err)

			if tc.dir.TakerPaysMPT != nil {
				require.NotNil(t, got.TakerPaysMPT)
				require.Equal(t, *tc.dir.TakerPaysMPT, *got.TakerPaysMPT)
				require.Equal(t, [20]byte{}, got.TakerPaysCurrency)
			}
			if tc.dir.TakerGetsMPT != nil {
				require.NotNil(t, got.TakerGetsMPT)
				require.Equal(t, *tc.dir.TakerGetsMPT, *got.TakerGetsMPT)
			} else {
				require.Nil(t, got.TakerGetsMPT)
				require.Equal(t, tc.dir.TakerGetsCurrency, got.TakerGetsCurrency)
				require.Equal(t, tc.dir.TakerGetsIssuer, got.TakerGetsIssuer)
			}

			// A second round-trip is byte-identical.
			data2, err := SerializeDirectoryNode(got, true)
			require.NoError(t, err)
			require.Equal(t, data, data2)
		})
	}
}

// TestMPTokenIssuanceReferenceHoldingRoundTrip covers the fixCleanup3_2_0
// vault-share sfReferenceHolding field on MPTokenIssuance.
func TestMPTokenIssuanceReferenceHoldingRoundTrip(t *testing.T) {
	ref := "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	iss := &MPTokenIssuanceData{
		Issuer:            [20]byte{9},
		Sequence:          1,
		OutstandingAmount: 100,
		ReferenceHolding:  &ref,
	}

	data, err := SerializeMPTokenIssuance(iss)
	require.NoError(t, err)

	got, err := ParseMPTokenIssuance(data)
	require.NoError(t, err)
	require.NotNil(t, got.ReferenceHolding)
	require.Equal(t, ref, strings.ToUpper(*got.ReferenceHolding))

	data2, err := SerializeMPTokenIssuance(got)
	require.NoError(t, err)
	require.Equal(t, data, data2)
}
