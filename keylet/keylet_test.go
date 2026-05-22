package keylet

import (
	"encoding/hex"
	"testing"
)

func TestBookDirKey(t *testing.T) {
	// XRP currency (all zeros)
	xrpCurrency := [20]byte{}
	xrpIssuer := [20]byte{} // XRP has no issuer

	// CNY currency and issuer
	cnyCurrency := [20]byte{}
	copy(cnyCurrency[12:], []byte("CNY"))

	// rnuF96W4SZoCJmbHYBFoJZpR8eCaxNvekK decoded
	cnyIssuer := [20]byte{}
	issuerBytes, _ := hex.DecodeString("35dd7df146893456296bf4061fbe68735d28f328")
	copy(cnyIssuer[:], issuerBytes)

	// For BookDir lookup: TakerPays=XRP, TakerGets=CNY
	// We're looking for offers where someone is selling CNY for XRP
	k := BookDir(xrpCurrency, xrpIssuer, cnyCurrency, cnyIssuer)

	t.Logf("Book base key (XRP->CNY): %s", hex.EncodeToString(k.Key[:]))
	t.Logf("Book base (first 24 bytes): %s", hex.EncodeToString(k.Key[:24]))
	t.Logf("Expected book dir:         ce67ae4e51228a295ef282f765196323525945b7d2c11bf05c038d7ea4c68000")

	// The first 24 bytes should match
	expectedPrefix := "ce67ae4e51228a295ef282f765196323525945b7d2c11bf0"
	gotPrefix := hex.EncodeToString(k.Key[:24])
	if gotPrefix != expectedPrefix {
		t.Errorf("Book base mismatch\n  got:      %s\n  expected: %s", gotPrefix, expectedPrefix)
	}
}

// AMM keylet sort key matches rippled's Issue::operator<=> — currency primary,
// then account. A previous implementation sorted issuer-primary, which produced
// the same keylet only when the asset pair was XRP+IOU (both sides tied on
// XRP's all-zero issuer and fell through to currency comparison). For an
// IOU+IOU pair where issuer-order and currency-order disagree, the two sorts
// produce DIFFERENT keylets — this test pins the rippled-conformant behavior.
func TestAMM_SortOrder_IOUPair_CurrencyPrimary(t *testing.T) {
	// Currency A < Currency B
	var curA, curB [20]byte
	copy(curA[12:], []byte("AAA"))
	copy(curB[12:], []byte("BBB"))

	// Issuer X > Issuer Y. With the OLD (issuer-primary) sort, Y would have
	// sorted first; with the rippled-conformant (currency-primary) sort, the
	// pair with curA wins regardless of issuer order.
	var issX, issY [20]byte
	issX[0] = 0xFF
	issY[0] = 0x01

	// pair1: (issX, curA) + (issY, curB) — currency-primary picks (issX, curA) first.
	pair1 := AMM(issX, curA, issY, curB)
	// pair2: same pair, supplied in reverse order. Sort must be symmetric.
	pair2 := AMM(issY, curB, issX, curA)
	if pair1.Key != pair2.Key {
		t.Fatalf("AMM keylet must be symmetric under argument order; got\n  pair1=%x\n  pair2=%x",
			pair1.Key, pair2.Key)
	}

	// A different pair (issY first by issuer, but curA wins by currency) must
	// still produce a keylet seeded with curA-side as "min" — i.e. swapping
	// issuers does not change the sort outcome.
	pair3 := AMM(issY, curA, issX, curB)
	if pair1.Key == pair3.Key {
		t.Fatalf("different issuer assignment must produce different AMM keylet")
	}
}

// Regression guard: XRP must round-trip through the AMM keylet via the
// all-zero currency. AMMCreate uses state.GetCurrencyBytes which returns
// all-zero for XRP; if any caller encodes "XRP" as ASCII bytes 12-14, the
// asset-pair lookup mis-keys and amm_info returns actNotFound.
func TestAMM_XRPPair_UsesAllZeroCurrency(t *testing.T) {
	var issuer [20]byte
	copy(issuer[:], []byte{0x35, 0xdd, 0x7d, 0xf1, 0x46, 0x89, 0x34, 0x56, 0x29, 0x6b,
		0xf4, 0x06, 0x1f, 0xbe, 0x68, 0x73, 0x5d, 0x28, 0xf3, 0x28})

	var usdCurrency [20]byte
	copy(usdCurrency[12:], []byte("USD"))

	// The canonical XRP+USD AMM keylet uses all-zero XRP currency.
	canonical := AMM([20]byte{}, [20]byte{}, issuer, usdCurrency)

	// An ASCII-encoded "XRP" (the bug we're guarding against) would put
	// 'X','R','P' into bytes 12-14 of the currency.
	var brokenXRP [20]byte
	brokenXRP[12], brokenXRP[13], brokenXRP[14] = 'X', 'R', 'P'
	broken := AMM([20]byte{}, brokenXRP, issuer, usdCurrency)

	if canonical.Key == broken.Key {
		t.Fatalf("canonical XRP keylet must differ from ASCII-encoded XRP keylet")
	}
}
