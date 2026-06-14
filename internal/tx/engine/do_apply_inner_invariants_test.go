package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// innerInvariantEngine builds a minimal engine over a mock base view for
// exercising CheckInnerInvariants in isolation.
func innerInvariantEngine(base *mockBaseView) *Engine {
	return NewEngine(base, txcore.EngineConfig{
		LedgerSequence: 100,
		Rules:          amendment.AllSupportedRules(),
	})
}

func mustAccount(t *testing.T, addr string, balance uint64, seq uint32) []byte {
	t.Helper()
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  addr,
		Balance:  balance,
		Sequence: seq,
	})
	if err != nil {
		t.Fatalf("serialize account: %v", err)
	}
	return data
}

const (
	innerInvSender    = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	innerInvRecipient = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"
)

// TestCheckInnerInvariants_LegitimateCreatePasses verifies that a single inner
// Payment that funds a new account — debiting the sender by exactly the credited
// amount — passes the per-inner invariant pass under the inner Payment type.
// This is the per-inner analog of the issue #846 false positive: each inner pass
// sees exactly one creation and a net-zero XRP delta.
func TestCheckInnerInvariants_LegitimateCreatePasses(t *testing.T) {
	base := newMockBaseView()
	senderID, _ := state.DecodeAccountID(innerInvSender)
	recipientID, _ := state.DecodeAccountID(innerInvRecipient)
	senderKey := keylet.Account(senderID)
	recipientKey := keylet.Account(recipientID)

	// Sender starts at 10_000 drops; base reflects the pre-payment state.
	base.data[senderKey.Key] = mustAccount(t, innerInvSender, 10_000, 1)

	e := innerInvariantEngine(base)
	table := txcore.NewApplyStateTable(base, [32]byte{}, 100, e.rules())

	// Inner Payment of 1_000 drops: debit sender, create recipient with 1_000.
	if err := table.Update(senderKey, mustAccount(t, innerInvSender, 9_000, 2)); err != nil {
		t.Fatalf("update sender: %v", err)
	}
	if err := table.Insert(recipientKey, mustAccount(t, innerInvRecipient, 1_000, 100)); err != nil {
		t.Fatalf("insert recipient: %v", err)
	}

	innerTx := txcore.NewBaseTx(txcore.TypePayment, innerInvSender)
	if got := e.CheckInnerInvariants(innerTx, ter.TesSUCCESS, table); got != ter.TesSUCCESS {
		t.Fatalf("legitimate inner create: expected tesSUCCESS, got %s", got)
	}
}

// TestCheckInnerInvariants_XRPCreatedFails verifies that the per-inner pass
// catches an inner delta that conjures XRP — a created account with no
// offsetting debit on the sender. rippled's XRPNotCreated rejects this on the
// inner tx's own perTxBatchView; goXRPL must do the same per inner tx.
func TestCheckInnerInvariants_XRPCreatedFails(t *testing.T) {
	base := newMockBaseView()
	senderID, _ := state.DecodeAccountID(innerInvSender)
	recipientID, _ := state.DecodeAccountID(innerInvRecipient)
	senderKey := keylet.Account(senderID)
	recipientKey := keylet.Account(recipientID)

	base.data[senderKey.Key] = mustAccount(t, innerInvSender, 10_000, 1)

	e := innerInvariantEngine(base)
	table := txcore.NewApplyStateTable(base, [32]byte{}, 100, e.rules())

	// Create the recipient with 1_000 drops but DO NOT debit the sender — this
	// is +1_000 drops of XRP from nothing.
	if err := table.Insert(recipientKey, mustAccount(t, innerInvRecipient, 1_000, 100)); err != nil {
		t.Fatalf("insert recipient: %v", err)
	}

	innerTx := txcore.NewBaseTx(txcore.TypePayment, innerInvSender)
	got := e.CheckInnerInvariants(innerTx, ter.TesSUCCESS, table)
	if got == ter.TesSUCCESS {
		t.Fatal("XRP-creating inner delta: expected invariant failure, got tesSUCCESS")
	}
	if got != ter.TecINVARIANT_FAILED {
		t.Fatalf("XRP-creating inner delta: expected tecINVARIANT_FAILED, got %s", got)
	}
}

// TestCheckInnerInvariants_IllegalAccountCreatorFails verifies that the per-inner
// pass rejects an account created by a transaction type not on the
// ValidNewAccountRoot allow-list — proving the removed "Batch" carve-out is no
// longer needed: each inner creation is validated under its own real type.
func TestCheckInnerInvariants_IllegalAccountCreatorFails(t *testing.T) {
	base := newMockBaseView()
	senderID, _ := state.DecodeAccountID(innerInvSender)
	recipientID, _ := state.DecodeAccountID(innerInvRecipient)
	senderKey := keylet.Account(senderID)
	recipientKey := keylet.Account(recipientID)

	base.data[senderKey.Key] = mustAccount(t, innerInvSender, 10_000, 1)

	e := innerInvariantEngine(base)
	table := txcore.NewApplyStateTable(base, [32]byte{}, 100, e.rules())

	// A net-zero XRP delta (sender debited, recipient created) but under an
	// AccountDelete inner type, which may NOT create an account root.
	if err := table.Update(senderKey, mustAccount(t, innerInvSender, 9_000, 2)); err != nil {
		t.Fatalf("update sender: %v", err)
	}
	if err := table.Insert(recipientKey, mustAccount(t, innerInvRecipient, 1_000, 100)); err != nil {
		t.Fatalf("insert recipient: %v", err)
	}

	innerTx := txcore.NewBaseTx(txcore.TypeAccountDelete, innerInvSender)
	if got := e.CheckInnerInvariants(innerTx, ter.TesSUCCESS, table); got != ter.TecINVARIANT_FAILED {
		t.Fatalf("illegal inner account creator: expected tecINVARIANT_FAILED, got %s", got)
	}
}
