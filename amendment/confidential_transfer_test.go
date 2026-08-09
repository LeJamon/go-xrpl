package amendment

import "testing"

func TestConfidentialTransferRemainsUnsupportedUntilTransactionFamilyComplete(t *testing.T) {
	feature := FeatureByID(FeatureConfidentialTransfer)
	if feature == nil {
		t.Fatal("ConfidentialTransfer is not registered")
	}
	if feature.Supported != SupportedNo {
		t.Fatalf("ConfidentialTransfer support = %v, want SupportedNo", feature.Supported)
	}
	for _, supported := range SupportedFeatures() {
		if supported.ID == FeatureConfidentialTransfer {
			t.Fatal("ConfidentialTransfer advertised by incomplete transaction layer")
		}
	}
}
