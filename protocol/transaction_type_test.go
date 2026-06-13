package protocol

import "testing"

// TestTxTypeXChainAttestationNames pins the two attestation transaction types
// that the duplicated invariants TxType table had dropped (45 / 46). With the
// table single-sourced here, ValidNewAccountRoot's permitted-type branch for
// these is reachable instead of seeing "Unknown(45)".
func TestTxTypeXChainAttestationNames(t *testing.T) {
	if got := TxType(45).String(); got != "XChainAddClaimAttestation" {
		t.Errorf("TxType(45).String() = %q, want XChainAddClaimAttestation", got)
	}
	if got := TxType(46).String(); got != "XChainAddAccountCreateAttestation" {
		t.Errorf("TxType(46).String() = %q, want XChainAddAccountCreateAttestation", got)
	}
	if got := TxTypeXChainAddClaimAttestation; got != 45 {
		t.Errorf("TxTypeXChainAddClaimAttestation = %d, want 45", got)
	}
	if got := TxTypeXChainAddAccountCreateAttest; got != 46 {
		t.Errorf("TxTypeXChainAddAccountCreateAttest = %d, want 46", got)
	}
}

// TestTxTypeNameRoundTrip checks the name↔code map is consistent for every
// non-deprecated type the String() method names.
func TestTxTypeNameRoundTrip(t *testing.T) {
	for name, code := range txTypeNameMap {
		if got := code.String(); got != name {
			t.Errorf("String(%d) = %q, want %q", code, got, name)
		}
		back, ok := TxTypeFromName(name)
		if !ok || back != code {
			t.Errorf("TxTypeFromName(%q) = (%d, %v), want (%d, true)", name, back, ok, code)
		}
	}
}

func TestTxTypeClassification(t *testing.T) {
	if !TxTypeAmendment.IsPseudoTransaction() {
		t.Error("EnableAmendment must be a pseudo-transaction")
	}
	if TxTypePayment.IsPseudoTransaction() {
		t.Error("Payment must not be a pseudo-transaction")
	}
	if !TxTypeNickNameSet.IsDeprecated() {
		t.Error("NickNameSet must be deprecated")
	}
	if TxTypePayment.IsDeprecated() {
		t.Error("Payment must not be deprecated")
	}
}
