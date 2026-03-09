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
