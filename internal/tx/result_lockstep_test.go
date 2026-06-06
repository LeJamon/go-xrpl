package tx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

// TestResultCodesLockstepWithDefinitions guards the invariant that every
// transaction result code go-xrpl can emit is serializable, by name, through
// definitions.json's TRANSACTION_RESULTS map — with the same numeric value.
//
// Transaction metadata serializes the TransactionResult by *name* via
// definitions.json. A code that exists in resultNames but is missing from — or
// renumbered relative to — TRANSACTION_RESULTS makes metadata serialization
// fail, which ships an empty meta blob *after* the transaction has already
// mutated ledger state. That is an account_hash / transaction_hash divergence:
// a hard fork that ordinary per-tx tests don't catch, because the state
// mutation still happens and only the (separately built) ledger hashes drift.
// This is the bug class fixed in PR #726; the check mechanizes it so a new or
// renamed tec/tem/ter can never merge without its definitions.json entry.
func TestResultCodesLockstepWithDefinitions(t *testing.T) {
	defs := definitions.Get()
	if len(defs.TransactionResults) == 0 {
		t.Fatal("definitions.TransactionResults is empty — definitions.json failed to load")
	}

	for code, name := range resultNames {
		got, ok := defs.TransactionResults[name]
		if !ok {
			t.Errorf("result %s (%d) is in resultNames but missing from definitions.json "+
				"TRANSACTION_RESULTS: metadata serialization of this code will fail and "+
				"silently fork the ledger — add it to definitions.json", name, int(code))
			continue
		}
		if got != int32(code) {
			t.Errorf("result %s value mismatch: result.go=%d definitions.json=%d "+
				"(a renumbered code serializes under the wrong name)", name, int(code), got)
		}
	}
}
