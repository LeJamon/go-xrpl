package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

const pdOwnerAddr = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

// validDomainBlob serializes a PermissionedDomain with a single (trivially
// sorted and unique) accepted credential.
func validDomainBlob(t *testing.T) []byte {
	t.Helper()
	var issuer [20]byte
	for i := range issuer {
		issuer[i] = 0x01
	}
	pd := &state.PermissionedDomainData{
		Owner:    state.GetIssuerBytes(pdOwnerAddr),
		Sequence: 1,
		AcceptedCredentials: []state.PermissionedDomainCredential{
			{Issuer: issuer, CredentialType: []byte("KYC")},
		},
	}
	b, err := state.SerializePermissionedDomain(pd, pdOwnerAddr)
	if err != nil {
		t.Fatalf("SerializePermissionedDomain: %v", err)
	}
	return b
}

// unsortedDomainBlob serializes a PermissionedDomain whose credentials are not
// in sorted (Issuer, CredentialType) order — an invalid domain.
func unsortedDomainBlob(t *testing.T) []byte {
	t.Helper()
	var issuerA, issuerB [20]byte
	for i := range issuerA {
		issuerA[i] = 0x01
		issuerB[i] = 0x02
	}
	pd := &state.PermissionedDomainData{
		Owner:    state.GetIssuerBytes(pdOwnerAddr),
		Sequence: 1,
		AcceptedCredentials: []state.PermissionedDomainCredential{
			{Issuer: issuerB, CredentialType: []byte("x")},
			{Issuer: issuerA, CredentialType: []byte("x")},
		},
	}
	b, err := state.SerializePermissionedDomain(pd, pdOwnerAddr)
	if err != nil {
		t.Fatalf("SerializePermissionedDomain: %v", err)
	}
	return b
}

// TestValidPermissionedDomain_PreAmendment pins the pre-fixCleanup3_1_3 behaviour:
// only a PermissionedDomainSet with tesSUCCESS is checked; any other transaction
// touching a domain is ignored.
func TestValidPermissionedDomain_PreAmendment(t *testing.T) {
	valid := validDomainBlob(t)
	bad := unsortedDomainBlob(t)

	tests := []struct {
		name    string
		txType  TxType
		result  Result
		entries []InvariantEntry
		wantV   bool
	}{
		{"PDSet valid domain", TypePermissionedDomainSet, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", After: valid}}, false},
		{"PDSet unsorted domain", TypePermissionedDomainSet, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", After: bad}}, true},
		{"Payment touching domain ignored", TypePayment, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", After: valid}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := checkValidPermissionedDomain(stubTx{txType: tc.txType}, tc.result, tc.entries, nil)
			if (v != nil) != tc.wantV {
				t.Fatalf("got violation=%v, want %v (%v)", v != nil, tc.wantV, v)
			}
		})
	}
}

// TestValidPermissionedDomain_PostAmendment pins the fixCleanup3_1_3 rewrite:
// failed transactions must not touch a domain; at most one domain may change;
// PermissionedDomainSet must leave one valid non-deleted domain;
// PermissionedDomainDelete must delete one; any other transaction must not touch
// a domain.
func TestValidPermissionedDomain_PostAmendment(t *testing.T) {
	valid := validDomainBlob(t)
	bad := unsortedDomainBlob(t)
	rules := cleanup313Rules()

	set := TypePermissionedDomainSet
	del := TypePermissionedDomainDelete

	tests := []struct {
		name    string
		txType  TxType
		result  Result
		entries []InvariantEntry
		wantV   bool
	}{
		{"PDSet creates valid domain", set, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", After: valid}}, false},
		{"PDSet unsorted domain fails", set, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", After: bad}}, true},
		{"PDSet touches no domain fails", set, TesSUCCESS, nil, true},
		{"PDSet deletes domain fails", set, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", Before: valid, IsDelete: true}}, true},
		{"PDDelete deletes domain ok", del, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", Before: valid, IsDelete: true}}, false},
		{"PDDelete modifies (not delete) fails", del, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", Before: valid, After: valid}}, true},
		{"PDDelete touches no domain fails", del, TesSUCCESS, nil, true},
		{"Payment touching domain fails", TypePayment, TesSUCCESS,
			[]InvariantEntry{{EntryType: "PermissionedDomain", After: valid}}, true},
		{"Payment not touching domain ok", TypePayment, TesSUCCESS, nil, false},
		{"failed PDSet touching domain fails", set, TecINCOMPLETE,
			[]InvariantEntry{{EntryType: "PermissionedDomain", After: valid}}, true},
		{"failed PDSet touching nothing ok", set, TecINCOMPLETE, nil, false},
		{"two domains affected fails", set, TesSUCCESS,
			[]InvariantEntry{
				{EntryType: "PermissionedDomain", After: valid},
				{EntryType: "PermissionedDomain", After: valid},
			}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := checkValidPermissionedDomain(stubTx{txType: tc.txType}, tc.result, tc.entries, rules)
			if (v != nil) != tc.wantV {
				t.Fatalf("got violation=%v, want %v (%v)", v != nil, tc.wantV, v)
			}
		})
	}
}
