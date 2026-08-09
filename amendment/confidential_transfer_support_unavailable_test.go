//go:build !mptcrypto || !cgo

package amendment

import "testing"

func TestConfidentialTransferUnsupportedWithoutNativeBackend(t *testing.T) {
	feature := FeatureByID(FeatureConfidentialTransfer)
	if feature == nil || feature.Supported != SupportedNo {
		t.Fatalf("ConfidentialTransfer feature = %+v, want SupportedNo", feature)
	}
	if feature.Vote != VoteDefaultNo {
		t.Fatalf("ConfidentialTransfer vote = %v, want VoteDefaultNo", feature.Vote)
	}
	for _, supported := range SupportedFeatures() {
		if supported.ID == FeatureConfidentialTransfer {
			t.Fatal("ConfidentialTransfer advertised without native backend")
		}
	}
}
