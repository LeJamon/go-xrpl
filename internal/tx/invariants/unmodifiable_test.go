package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func rulesWithLendingProtocol() *amendment.Rules {
	return amendment.NewRules([][32]byte{amendment.FeatureLendingProtocol})
}

func encodeSLE(t *testing.T, m map[string]any) []byte {
	t.Helper()
	h, err := binarycodec.Encode(m)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return b
}

func accountRootMap() map[string]any {
	return map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           testPseudoAddr,
		"Balance":           "1000000",
		"Sequence":          uint32(1),
		"OwnerCount":        uint32(0),
		"Flags":             uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	}
}

// TestNoModifiedUnmodifiableFields_LedgerEntryType: changing the ledger entry
// type of a modified object trips the invariant.
// Reference: rippled Invariants_test.cpp:1902-1924.
func TestNoModifiedUnmodifiableFields_LedgerEntryType(t *testing.T) {
	before := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 1_000_000, Sequence: 1})
	// LedgerEntryType is always the first serialized field: header 0x11 followed
	// by the 2-byte big-endian type code. Bump the low byte to forge a change.
	after := append([]byte(nil), before...)
	after[2]++

	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}
	if v := checkNoModifiedUnmodifiableFields(entries, rulesWithLendingProtocol()); v == nil {
		t.Fatal("expected violation for changed LedgerEntryType")
	}
}

// TestNoModifiedUnmodifiableFields_LedgerIndex: adding an sfLedgerIndex field on
// modification trips the invariant.
// Reference: rippled Invariants_test.cpp:1905-1908.
func TestNoModifiedUnmodifiableFields_LedgerIndex(t *testing.T) {
	before := encodeSLE(t, accountRootMap())
	afterMap := accountRootMap()
	afterMap["LedgerIndex"] = strings.Repeat("0", 63) + "1"
	after := encodeSLE(t, afterMap)

	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}
	if v := checkNoModifiedUnmodifiableFields(entries, rulesWithLendingProtocol()); v == nil {
		t.Fatal("expected violation for added LedgerIndex")
	}
}

// TestNoModifiedUnmodifiableFields_NoChange: a modification that leaves the
// entry type and index untouched is fine.
func TestNoModifiedUnmodifiableFields_NoChange(t *testing.T) {
	before := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 1_000_000, Sequence: 1})
	after := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 999_990, Sequence: 2})
	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}
	if v := checkNoModifiedUnmodifiableFields(entries, rulesWithLendingProtocol()); v != nil {
		t.Fatalf("unexpected violation for ordinary modification: %v", v)
	}
}

// TestNoModifiedUnmodifiableFields_IgnoresCreateDelete: creation and deletion
// are not inspected.
func TestNoModifiedUnmodifiableFields_IgnoresCreateDelete(t *testing.T) {
	acct := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 1_000_000, Sequence: 1})
	rules := rulesWithLendingProtocol()
	if v := checkNoModifiedUnmodifiableFields([]InvariantEntry{{EntryType: entry.TypeAccountRoot, After: acct}}, rules); v != nil {
		t.Fatalf("creation: unexpected violation %v", v)
	}
	if v := checkNoModifiedUnmodifiableFields([]InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: acct, IsDelete: true}}, rules); v != nil {
		t.Fatalf("deletion: unexpected violation %v", v)
	}
}

// TestNoModifiedUnmodifiableFields_Gating: while featureLendingProtocol is
// disabled the check never fails a transaction.
func TestNoModifiedUnmodifiableFields_Gating(t *testing.T) {
	before := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 1_000_000, Sequence: 1})
	after := append([]byte(nil), before...)
	after[2]++
	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}
	if v := checkNoModifiedUnmodifiableFields(entries, amendment.NewRules(nil)); v != nil {
		t.Fatalf("disabled amendment: unexpected violation %v", v)
	}
}
