//go:build mptcrypto && cgo

package amendment

import "testing"

func TestConfidentialTransferSupportedWithNativeBackend(t *testing.T) {
	feature := FeatureByID(FeatureConfidentialTransfer)
	if feature == nil || feature.Supported != SupportedYes {
		t.Fatalf("ConfidentialTransfer feature = %+v, want SupportedYes", feature)
	}
	if feature.Vote != VoteDefaultNo {
		t.Fatalf("ConfidentialTransfer vote = %v, want VoteDefaultNo", feature.Vote)
	}
	found := false
	for _, supported := range SupportedFeatures() {
		found = found || supported.ID == FeatureConfidentialTransfer
	}
	if !found {
		t.Fatal("ConfidentialTransfer omitted from supported features with native backend")
	}
}

func TestConfidentialTransferUnsupportedWithoutNativeContext(t *testing.T) {
	original := confidentialTransferBackendAvailable
	confidentialTransferBackendAvailable = func() bool { return false }
	t.Cleanup(func() { confidentialTransferBackendAvailable = original })

	if got := confidentialTransferSupport(); got != SupportedNo {
		t.Fatalf("ConfidentialTransfer support = %v, want SupportedNo", got)
	}
}
