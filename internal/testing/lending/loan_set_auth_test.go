package lending_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestLoanSetMPTAuthorization(t *testing.T) {
	tests := []struct {
		name              string
		authorizeBorrower bool
		originationFee    bool
		ownerSubmits      bool
		unauthorized      func(*loanSetAssetFixture) *jtx.Account
	}{
		{
			name:         "borrower submits",
			unauthorized: func(f *loanSetAssetFixture) *jtx.Account { return f.borrower },
		},
		{
			name:         "broker owner submits for unauthorized borrower",
			ownerSubmits: true,
			unauthorized: func(f *loanSetAssetFixture) *jtx.Account { return f.borrower },
		},
		{
			name:              "origination fee recipient",
			authorizeBorrower: true,
			originationFee:    true,
			unauthorized:      func(f *loanSetAssetFixture) *jtx.Account { return f.owner },
		},
		{
			name:              "broker owner without origination fee",
			authorizeBorrower: true,
			unauthorized:      func(f *loanSetAssetFixture) *jtx.Account { return f.owner },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoanSetAssetFixture(t, "MPT", mpttest.TfMPTRequireAuth)
			if tc.authorizeBorrower {
				f.createHolding(f.borrower)
			}
			submitter, counterparty := f.borrower, f.owner
			if tc.ownerSubmits {
				submitter, counterparty = counterparty, submitter
			}

			beforeState := loanSetLedgerState(t, f.env)
			beforeBalance := f.env.Balance(submitter)
			beforeSequence := f.env.Seq(submitter)
			beforeBorrowerOwners := f.env.OwnerCount(f.borrower)
			beforeOwnerOwners := f.env.OwnerCount(f.owner)

			result := submitLoanSet(t, f, submitter, counterparty, tc.originationFee)
			jtx.RequireTxClaimed(t, result, jtx.TecNO_AUTH)
			if !result.Applied || result.Fee != 20 {
				t.Fatalf("applied/fee = %v/%d, want true/20", result.Applied, result.Fee)
			}
			if result.Metadata == nil || result.Metadata.TransactionResult.String() != jtx.TecNO_AUTH {
				t.Fatalf("metadata result = %v, want %s", result.Metadata, jtx.TecNO_AUTH)
			}
			if len(result.Metadata.AffectedNodes) != 1 {
				t.Fatalf("affected nodes = %d, want 1", len(result.Metadata.AffectedNodes))
			}
			node := result.Metadata.AffectedNodes[0]
			accountKey := keylet.Account(submitter.AccountID())
			wantIndex := strings.ToUpper(hex.EncodeToString(accountKey.Key[:]))
			if node.NodeType != "ModifiedNode" || node.LedgerEntryType != "AccountRoot" || node.LedgerIndex != wantIndex {
				t.Fatalf("affected node = %+v, want submitter AccountRoot modification", node)
			}

			if got := f.env.Balance(submitter); got != beforeBalance-20 {
				t.Errorf("submitter balance = %d, want %d", got, beforeBalance-20)
			}
			if got := f.env.Seq(submitter); got != beforeSequence+1 {
				t.Errorf("submitter sequence = %d, want %d", got, beforeSequence+1)
			}
			if got := f.env.OwnerCount(f.borrower); got != beforeBorrowerOwners {
				t.Errorf("borrower OwnerCount = %d, want %d", got, beforeBorrowerOwners)
			}
			if got := f.env.OwnerCount(f.owner); got != beforeOwnerOwners {
				t.Errorf("broker owner OwnerCount = %d, want %d", got, beforeOwnerOwners)
			}
			if f.env.LedgerEntryExists(f.holdingKey(tc.unauthorized(f))) {
				t.Error("unauthorized MPToken remained after claimed failure")
			}
			if f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)) {
				t.Error("Loan remained after claimed failure")
			}

			afterState := loanSetLedgerState(t, f.env)
			delete(beforeState, accountKey.Key)
			delete(afterState, accountKey.Key)
			if len(afterState) != len(beforeState) {
				t.Fatalf("non-submitter state entry count = %d, want %d", len(afterState), len(beforeState))
			}
			for key, want := range beforeState {
				got, ok := afterState[key]
				if !ok || !bytes.Equal(got, want) {
					t.Fatalf("non-submitter ledger entry %X changed after claimed failure", key)
				}
			}
		})
	}

	t.Run("authorized parties", func(t *testing.T) {
		f := newLoanSetAssetFixture(t, "MPT", mpttest.TfMPTRequireAuth)
		f.createHolding(f.borrower)
		f.createHolding(f.owner)
		jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, true))
		if !f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)) {
			t.Fatal("Loan was not created")
		}
	})
}

func loanSetLedgerState(t *testing.T, env *jtx.TestEnv) map[[32]byte][]byte {
	t.Helper()
	entries := make(map[[32]byte][]byte)
	if err := env.Ledger().ForEach(func(key [32]byte, data []byte) bool {
		entries[key] = append([]byte(nil), data...)
		return true
	}); err != nil {
		t.Fatalf("snapshot ledger state: %v", err)
	}
	return entries
}
