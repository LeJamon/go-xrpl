package state

import (
	"strings"
	"testing"
)

func nftHexUpper(b []byte) string { return strings.ToUpper(hexEncode(b)) }

func hexEncode(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = h[v>>4]
		out[i*2+1] = h[v&0x0f]
	}
	return string(out)
}

// TestSerializeNFTokenPage_Golden locks the NFTokenPage SLE bytes.
func TestSerializeNFTokenPage_Golden(t *testing.T) {
	page := &NFTokenPageData{
		PreviousPageMin:   [32]byte{0x11, 0x22},
		NextPageMin:       [32]byte{0x33, 0x44},
		PreviousTxnID:     [32]byte{0xaa, 0xbb},
		PreviousTxnLgrSeq: 123,
		NFTokens: []NFTokenData{
			{NFTokenID: [32]byte{0x01, 0x02, 0x03}, URI: "68747470"},
			{NFTokenID: [32]byte{0x0a, 0x0b}},
		},
	}
	got, err := SerializeNFTokenPage(page)
	if err != nil {
		t.Fatalf("SerializeNFTokenPage: %v", err)
	}
	const want = "1100502200000000250000007B55AABB000000000000000000000000000000000000000000000000000000000000501A1122000000000000000000000000000000000000000000000000000000000000501B3344000000000000000000000000000000000000000000000000000000000000FAEC5A0102030000000000000000000000000000000000000000000000000000000000750468747470E1EC5A0A0B000000000000000000000000000000000000000000000000000000000000E1F1"
	if gotHex := nftHexUpper(got); gotHex != want {
		t.Fatalf("byte divergence:\n got=%s\nwant=%s", gotHex, want)
	}
}

// TestSerializeNFTokenOffer_Golden locks the NFTokenOffer SLE bytes for XRP and IOU offers.
func TestSerializeNFTokenOffer_Golden(t *testing.T) {
	var owner [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
	}
	tokenID := [32]byte{0xde, 0xad, 0xbe, 0xef}
	exp := uint32(700000000)

	xrp, err := SerializeNFTokenOffer(owner, tokenID, "1000000", 1, 3, 5, "", nil)
	if err != nil {
		t.Fatalf("SerializeNFTokenOffer(xrp): %v", err)
	}
	const wantXRP = "11003722000000013400000000000000033C00000000000000055ADEADBEEF000000000000000000000000000000000000000000000000000000006140000000000F424082140102030405060708090A0B0C0D0E0F1011121314"
	if gotHex := nftHexUpper(xrp); gotHex != wantXRP {
		t.Fatalf("offer_xrp byte divergence:\n got=%s\nwant=%s", gotHex, wantXRP)
	}

	iou := map[string]any{"value": "50", "currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}
	full, err := SerializeNFTokenOffer(owner, tokenID, iou, 0, 7, 9, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", &exp)
	if err != nil {
		t.Fatalf("SerializeNFTokenOffer(iou): %v", err)
	}
	const wantIOU = "11003722000000002A29B927003400000000000000073C00000000000000095ADEADBEEF0000000000000000000000000000000000000000000000000000000061D4D1C37937E080000000000000000000000000005553440000000000B5F762798A53D543A014CAF8B297CFF8F2F937E882140102030405060708090A0B0C0D0E0F10111213148314B5F762798A53D543A014CAF8B297CFF8F2F937E8"
	if gotHex := nftHexUpper(full); gotHex != wantIOU {
		t.Fatalf("offer_iou byte divergence:\n got=%s\nwant=%s", gotHex, wantIOU)
	}
}
