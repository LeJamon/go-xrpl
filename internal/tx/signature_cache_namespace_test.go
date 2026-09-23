package tx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
)

func TestCommonSignatureCacheNamespaces(t *testing.T) {
	oldRules := amendment.NewRules(nil)
	newRules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	var txID [32]byte
	txID[0] = 1

	role := &Common{CounterpartySignature: &CounterpartySignature{}}
	if !role.SignatureCacheLegacyRole(oldRules) {
		t.Fatal("role-bearing transaction must use the legacy namespace before cleanup")
	}
	if role.SignatureCacheLegacyRole(newRules) {
		t.Fatal("role-bearing transaction must use the normal namespace after cleanup")
	}
	role.MarkSignatureVerifiedWithRules(txID, oldRules)
	if !role.SignatureVerifiedWithRules(txID, oldRules) {
		t.Fatal("legacy-role verdict must hit under the rules that recorded it")
	}
	if role.SignatureVerifiedWithRules(txID, newRules) {
		t.Fatal("legacy-role verdict must miss under cleanup rules")
	}

	role.MarkSignatureVerifiedWithRules(txID, newRules)
	if !role.SignatureVerifiedWithRules(txID, newRules) {
		t.Fatal("normal role verdict must hit under cleanup rules")
	}
	if role.SignatureVerifiedWithRules(txID, oldRules) {
		t.Fatal("normal role verdict must miss under pre-cleanup rules")
	}

	ordinary := &Common{}
	ordinary.MarkSignatureVerifiedWithRules(txID, oldRules)
	if !ordinary.SignatureVerifiedWithRules(txID, newRules) {
		t.Fatal("ordinary verdict must share the normal namespace across rules")
	}

	presentOnly := &Common{PresentFields: map[string]bool{"SponsorSignature": true}}
	if !presentOnly.SignatureCacheLegacyRole(oldRules) {
		t.Fatal("wire-presence of a role field must select the legacy namespace")
	}
}

func TestCommonLegacySignatureVerifiedWrapperRemainsIDBased(t *testing.T) {
	var txID [32]byte
	txID[0] = 2
	common := &Common{SponsorSignature: &SponsorSignature{}}
	common.MarkSignatureVerified(txID)
	if !common.SignatureVerified(txID) {
		t.Fatal("legacy signature verdict wrapper must retain its ID-based behavior")
	}
}
