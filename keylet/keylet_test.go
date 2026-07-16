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
// all-zero currency. AMMCreate uses keylet.CurrencyBytes which returns
// all-zero for XRP; if any caller encodes "XRP" as ASCII bytes 12-14, the
// asset-pair lookup mis-keys and amm_info returns actNotFound.
func TestAMM_XRPPair_UsesAllZeroCurrency(t *testing.T) {
	var issuer [20]byte
	copy(issuer[:], []byte{0x35, 0xdd, 0x7d, 0xf1, 0x46, 0x89, 0x34, 0x56, 0x29, 0x6b,
		0xf4, 0x06, 0x1f, 0xbe, 0x68, 0x73, 0x5d, 0x28, 0xf3, 0x28})

	var usdCurrency [20]byte
	copy(usdCurrency[12:], []byte("USD"))

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

// Mirrors rippled's Issue::operator<=> XRP shortcut
// (rippled/include/xrpl/protocol/Issue.h:104): on a currency tie, if the
// currency is XRP the comparison returns equivalent without touching the
// account, and std::minmax keeps original argument order. No real caller
// can produce an XRP/XRP AMM with a non-zero issuer (XRP's issuer is always
// all-zero), but this test pins the literal port: swapping args must NOT be
// "normalized" by the keylet — the hash should change, exactly as rippled's
// does. The previous Go implementation incorrectly produced symmetric
// keylets here by falling through to compare issuers.
func TestAMM_SortOrder_XRPCurrencyTie_KeepsOriginalOrder(t *testing.T) {
	xrp := [20]byte{}

	var issA, issB [20]byte
	issA[0] = 0x01
	issB[0] = 0xFF

	k1 := AMM(issA, xrp, issB, xrp)
	k2 := AMM(issB, xrp, issA, xrp)
	if k1.Key == k2.Key {
		t.Fatalf("XRP/XRP tie with distinct issuers must NOT be normalized — "+
			"rippled returns weak_ordering::equivalent and std::minmax keeps "+
			"the original arg order, so the hashes differ; got\n  k1=%x\n  k2=%x",
			k1.Key, k2.Key)
	}
}

func TestXChainClaimKeyletsHashRawBridgeFields(t *testing.T) {
	var bridge XChainBridge
	for i := range bridge.LockingDoor {
		bridge.LockingDoor[i] = byte(i + 1)
		bridge.LockingIssuer[i] = byte(i + 0x21)
		bridge.IssuingDoor[i] = byte(i + 0x41)
	}
	copy(bridge.LockingCurrency[12:], []byte("USD"))

	const sequence = uint64(0x0102030405060708)
	tests := []struct {
		name     string
		actual   [32]byte
		expected string
	}{
		{
			"claim",
			XChainClaimID(bridge, sequence).Key,
			"25e9bb7665ffbf5e0529daa2719490c7f3cb492f2e0c293e97084f6e39e691d7",
		},
		{
			"create account claim",
			XChainCreateAccountClaimID(bridge, sequence).Key,
			"165772e2dacbb6c064392e074876561376e06d10e5c3e860e5ac47fbf21063a2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if actual := hex.EncodeToString(tc.actual[:]); actual != tc.expected {
				t.Fatalf("unexpected XChain key: got %s, want %s", actual, tc.expected)
			}
		})
	}
}

func TestDepositPreauthKeylets(t *testing.T) {
	decodeAccount := func(encoded string) [20]byte {
		t.Helper()
		decoded, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode account ID %s: %v", encoded, err)
		}
		if len(decoded) != 20 {
			t.Fatalf("account ID %s decoded to %d bytes, want 20", encoded, len(decoded))
		}
		var account [20]byte
		copy(account[:], decoded)
		return account
	}

	owner := decodeAccount("09127e0295e2e0fcb76f70d816fed9029a771f30")
	issuer := decodeAccount("0811f666a9c82537a71592b583c4b78e4b7dcc2c")
	credentials := []CredentialPair{{Issuer: issuer, CredentialType: []byte("Administration")}}
	multipleCredentials := []CredentialPair{
		{Issuer: owner, CredentialType: []byte("KYC")},
		{Issuer: issuer, CredentialType: []byte("Administration")},
		{Issuer: issuer, CredentialType: []byte("Administration")},
	}

	tests := []struct {
		name     string
		key      Keylet
		expected string
	}{
		{"account", DepositPreauth(owner, issuer), "06d8409e9a8d44925723cddb021e1260dc294098da1d7b0eda2245557c622ca0"},
		{"empty credentials", DepositPreauthCredentials(owner, nil), "01f6ddb0c831858957ec1b50a82f7c703e45792b09034549ba5a394a26508337"},
		{"credentials", DepositPreauthCredentials(owner, credentials), "95a4bc7f742e6191c206128c655c59fdfd1344e5421b258536c4d00314e577a2"},
		{"multiple credentials", DepositPreauthCredentials(owner, multipleCredentials), "9bee57bf61f0bcb2e201abcc1014722491e75f23dcb41f00327c59804af81824"},
	}
	if multipleCredentials[0].Issuer != owner {
		t.Fatal("DepositPreauthCredentials mutated its input")
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if actual := hex.EncodeToString(tc.key.Key[:]); actual != tc.expected {
				t.Fatalf("DepositPreauth key = %s, want %s", actual, tc.expected)
			}
		})
	}
}
