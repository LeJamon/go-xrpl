package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestSortAssets_IssuerByteOrder verifies that two assets sharing a currency are
// ordered by decoded issuer AccountID bytes, matching rippled's
// std::minmax(Issue) — not by base58 string order. The two issuers below decode
// to AccountIDs whose byte order (A < B) is the reverse of their base58 string
// order (A > B, because uppercase sorts before lowercase): a string comparator
// would swap sfAsset/sfAsset2 relative to rippled and fork account_hash.
func TestSortAssets_IssuerByteOrder(t *testing.T) {
	mk := func(b0, b1 byte) [20]byte {
		var id [20]byte
		id[0], id[1], id[19] = b0, b1, 0x42
		return id
	}
	lowBytes := mk(0x01, 0x11)  // byte-smaller AccountID
	highBytes := mk(0x02, 0x22) // byte-larger AccountID

	lowAddr, err := state.EncodeAccountID(lowBytes)
	if err != nil {
		t.Fatal(err)
	}
	highAddr, err := state.EncodeAccountID(highBytes)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: byte order and base58 string order disagree.
	if !(lowAddr > highAddr) {
		t.Fatalf("expected base58 string order to disagree with byte order, got %s vs %s", lowAddr, highAddr)
	}

	assetLow := tx.Asset{Currency: "USD", Issuer: lowAddr}
	assetHigh := tx.Asset{Currency: "USD", Issuer: highAddr}
	amtLow := state.NewIssuedAmountFromValue(1, 0, "USD", lowAddr)
	amtHigh := state.NewIssuedAmountFromValue(1, 0, "USD", highAddr)

	// Regardless of input order, the byte-smaller issuer must come first.
	for _, tc := range []struct {
		name   string
		a1, a2 tx.Asset
		m1, m2 tx.Amount
	}{
		{"low-first", assetLow, assetHigh, amtLow, amtHigh},
		{"high-first", assetHigh, assetLow, amtHigh, amtLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s1, s2, sm1, sm2 := sortAssets(tc.a1, tc.a2, tc.m1, tc.m2)
			if s1.Issuer != lowAddr || s2.Issuer != highAddr {
				t.Fatalf("sfAsset/sfAsset2 ordering wrong: got (%s, %s), want (%s, %s)",
					s1.Issuer, s2.Issuer, lowAddr, highAddr)
			}
			if sm1.Issuer != lowAddr || sm2.Issuer != highAddr {
				t.Fatalf("amounts not reordered with assets")
			}
		})
	}
}

// TestSortAssets_ISOvsHexCurrency verifies currency ordering compares decoded
// 20-byte currency codes, not their string forms. The 40-hex currency below has
// a currency byte (0x99 at index 12) greater than "USD"'s (0x55), so by rippled's
// byte ordering it sorts AFTER USD; but as strings "0000..." sorts before "USD",
// so a string comparator would place it first and fork.
func TestSortAssets_ISOvsHexCurrency(t *testing.T) {
	const hexCurrency = "0000000000000000000000009900000000000000"
	issuer, err := state.EncodeAccountID([20]byte{0x07})
	if err != nil {
		t.Fatal(err)
	}

	usd := tx.Asset{Currency: "USD", Issuer: issuer}
	hexAsset := tx.Asset{Currency: hexCurrency, Issuer: issuer}
	usdAmt := state.NewIssuedAmountFromValue(1, 0, "USD", issuer)
	hexAmt := state.NewIssuedAmountFromValue(1, 0, hexCurrency, issuer)

	for _, tc := range []struct {
		name   string
		a1, a2 tx.Asset
		m1, m2 tx.Amount
	}{
		{"usd-first", usd, hexAsset, usdAmt, hexAmt},
		{"hex-first", hexAsset, usd, hexAmt, usdAmt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s1, s2, _, _ := sortAssets(tc.a1, tc.a2, tc.m1, tc.m2)
			if s1.Currency != "USD" || s2.Currency != hexCurrency {
				t.Fatalf("currency ordering wrong: got (%s, %s), want (USD, %s)",
					s1.Currency, s2.Currency, hexCurrency)
			}
		})
	}
}

// TestSortAssets_XRPFirst confirms XRP (all-zero currency bytes) sorts before any
// IOU, matching byte ordering.
func TestSortAssets_XRPFirst(t *testing.T) {
	issuer, _ := state.EncodeAccountID([20]byte{0x09})
	xrp := tx.Asset{Currency: "XRP"}
	usd := tx.Asset{Currency: "USD", Issuer: issuer}
	xrpAmt := state.NewXRPAmountFromInt(1)
	usdAmt := state.NewIssuedAmountFromValue(1, 0, "USD", issuer)

	s1, s2, _, _ := sortAssets(usd, xrp, usdAmt, xrpAmt)
	if s1.Currency != "XRP" || s2.Currency != "USD" {
		t.Fatalf("XRP should sort first: got (%s, %s)", s1.Currency, s2.Currency)
	}
}
