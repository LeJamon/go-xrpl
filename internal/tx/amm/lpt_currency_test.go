package amm

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestGenerateAMMLPTCurrency_XRP_USD(t *testing.T) {
	// The fixture at Invalid_Bid.json step 9 shows BidMin currency:
	// 03930D02208264E2E40EC1B0C09E4DB96EE197B1
	// This is the LP token currency for XRP/USD pair
	expected := "03930D02208264E2E40EC1B0C09E4DB96EE197B1"

	result := GenerateAMMLPTCurrency("XRP", "USD")
	t.Logf("GenerateAMMLPTCurrency(XRP, USD) = %s", result)
	if result != expected {
		t.Errorf("LP currency mismatch:\n  got:  %s\n  want: %s", result, expected)
	}

	// Also test with empty string for XRP
	result2 := GenerateAMMLPTCurrency("", "USD")
	t.Logf("GenerateAMMLPTCurrency('', USD)  = %s", result2)
	if result2 != expected {
		t.Errorf("LP currency mismatch with empty XRP:\n  got:  %s\n  want: %s", result2, expected)
	}
}

func TestGenerateAMMLPTCurrency_MPT_XRP(t *testing.T) {
	idHex := "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"
	id, err := hex.DecodeString(idHex)
	if err != nil {
		t.Fatal(err)
	}
	xrp := keylet.CurrencyBytes("XRP")
	hash := sha512half.Sum(id, xrp[:])
	var currency [20]byte
	currency[0] = 3
	copy(currency[1:], hash[:19])
	expected := strings.ToUpper(hex.EncodeToString(currency[:]))

	mpt := tx.Asset{MPTIssuanceID: idHex}
	xrpAsset := tx.Asset{Currency: "XRP"}
	if got := GenerateAMMLPTCurrencyForAssets(mpt, xrpAsset); got != expected {
		t.Fatalf("MPT/XRP LP currency: got %s, want %s", got, expected)
	}
	if got := GenerateAMMLPTCurrencyForAssets(xrpAsset, mpt); got != expected {
		t.Fatalf("LP currency must be symmetric: got %s, want %s", got, expected)
	}
}
