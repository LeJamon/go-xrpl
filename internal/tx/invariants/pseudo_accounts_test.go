package invariants

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const testPseudoAddr = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// validPseudoAccount returns an AccountRoot that satisfies every
// ValidPseudoAccounts rule: exactly one designator (VaultID), zero sequence and
// the canonical pseudo-account flag mask.
func validPseudoAccount() *state.AccountRoot {
	return &state.AccountRoot{
		Account:  testPseudoAddr,
		Balance:  0,
		Sequence: 0,
		Flags:    LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth,
		VaultID:  [32]byte{1},
	}
}

func rulesWithSingleAssetVaultOnly() *amendment.Rules {
	return amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
}

// TestValidPseudoAccounts_Valid: a well-formed pseudo-account (created or
// modified) trips no invariant.
func TestValidPseudoAccounts_Valid(t *testing.T) {
	rules := rulesWithSingleAssetVaultOnly()
	after := mustSerializeAccount(t, validPseudoAccount())

	// Creation.
	if v := checkValidPseudoAccounts([]InvariantEntry{{EntryType: entry.TypeAccountRoot, After: after}}, rules); v != nil {
		t.Fatalf("valid pseudo-account creation: unexpected violation %v", v)
	}
	// Idempotent modification (before == after).
	if v := checkValidPseudoAccounts([]InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: after, After: after}}, rules); v != nil {
		t.Fatalf("valid pseudo-account modification: unexpected violation %v", v)
	}
}

// TestValidPseudoAccounts_Violations ports the mutation cases from rippled's
// testValidPseudoAccounts (Invariants_test.cpp:1560-1640).
func TestValidPseudoAccounts_Violations(t *testing.T) {
	rules := rulesWithSingleAssetVaultOnly()
	before := mustSerializeAccount(t, validPseudoAccount())

	cases := []struct {
		name    string
		mutate  func(a *state.AccountRoot)
		wantMsg string
	}{
		{
			name:    "zero designator fields",
			mutate:  func(a *state.AccountRoot) { a.VaultID = [32]byte{} }, // Sequence stays 0 → still looks pseudo
			wantMsg: "0 pseudo-account fields set",
		},
		{
			name:    "two designator fields",
			mutate:  func(a *state.AccountRoot) { a.AMMID = [32]byte{2} },
			wantMsg: "2 pseudo-account fields set",
		},
		{
			name:    "sequence changed",
			mutate:  func(a *state.AccountRoot) { a.Sequence = 12345 },
			wantMsg: "sequence changed",
		},
		{
			name:    "flags not set",
			mutate:  func(a *state.AccountRoot) { a.Flags = LsfDisableMaster }, // missing DefaultRipple|DepositAuth
			wantMsg: "flags are not set",
		},
		{
			name:    "regular key present",
			mutate:  func(a *state.AccountRoot) { a.RegularKey = testPseudoAddr },
			wantMsg: "has a regular key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acct := validPseudoAccount()
			tc.mutate(acct)
			after := mustSerializeAccount(t, acct)
			v := checkValidPseudoAccounts([]InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}, rules)
			if v == nil {
				t.Fatalf("%s: expected violation", tc.name)
			}
			if !strings.Contains(v.Message, tc.wantMsg) {
				t.Fatalf("%s: message %q does not contain %q", tc.name, v.Message, tc.wantMsg)
			}
		})
	}
}

// TestValidPseudoAccounts_ZeroSequenceRegularAccount: a plain account whose
// Sequence is set to 0 looks like a pseudo-account and must trip the check
// (0 designator fields). Reference: Invariants_test.cpp:1627-1640.
func TestValidPseudoAccounts_ZeroSequenceRegularAccount(t *testing.T) {
	rules := rulesWithSingleAssetVaultOnly()
	before := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 1_000_000, Sequence: 7})
	after := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 1_000_000, Sequence: 0})
	v := checkValidPseudoAccounts([]InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}, rules)
	if v == nil {
		t.Fatal("expected violation for zero-sequence non-pseudo account")
	}
	if !strings.Contains(v.Message, "0 pseudo-account fields set") {
		t.Fatalf("unexpected message %q", v.Message)
	}
}

// TestValidPseudoAccounts_Gating: while featureSingleAssetVault is disabled the
// check is detection-only and never fails a transaction.
func TestValidPseudoAccounts_Gating(t *testing.T) {
	before := mustSerializeAccount(t, validPseudoAccount())
	broken := validPseudoAccount()
	broken.VaultID = [32]byte{} // 0 designator fields, still zero-sequence
	after := mustSerializeAccount(t, broken)

	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}

	if v := checkValidPseudoAccounts(entries, amendment.NewRules(nil)); v != nil {
		t.Fatalf("disabled amendment: unexpected violation %v", v)
	}
	if v := checkValidPseudoAccounts(entries, rulesWithSingleAssetVaultOnly()); v == nil {
		t.Fatal("enabled amendment: expected violation")
	}
}

// TestValidPseudoAccounts_IgnoresDeletion: deletions are not inspected.
func TestValidPseudoAccounts_IgnoresDeletion(t *testing.T) {
	broken := validPseudoAccount()
	broken.VaultID = [32]byte{}
	before := mustSerializeAccount(t, broken)
	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, IsDelete: true}}
	if v := checkValidPseudoAccounts(entries, rulesWithSingleAssetVaultOnly()); v != nil {
		t.Fatalf("deletion: unexpected violation %v", v)
	}
}
