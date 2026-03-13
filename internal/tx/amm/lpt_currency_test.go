package amm

import (
	"testing"
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
