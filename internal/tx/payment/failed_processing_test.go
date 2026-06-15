package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// TestFailedProcessingResult_ViewOpenness verifies the FAILED_PROCESSING TER
// is mutated by sandbox view openness, mirroring rippled View.cpp:
// view.open() ? telFAILED_PROCESSING : tecFAILED_PROCESSING.
func TestFailedProcessingResult_ViewOpenness(t *testing.T) {
	view := newPaymentMockLedgerView()

	closed := NewPaymentSandbox(view)
	if got := closed.failedProcessingResult(); got != ter.TecFAILED_PROCESSING {
		t.Fatalf("closed view: want tecFAILED_PROCESSING, got %s", got)
	}

	open := NewPaymentSandbox(view)
	open.SetOpenLedger(true)
	if got := open.failedProcessingResult(); got != ter.TelFAILED_PROCESSING {
		t.Fatalf("open view: want telFAILED_PROCESSING, got %s", got)
	}

	// Child sandboxes inherit openness from the parent.
	child := NewChildSandbox(open)
	if !child.IsOpenLedger() {
		t.Fatalf("child of open sandbox should report open ledger")
	}
	if got := child.failedProcessingResult(); got != ter.TelFAILED_PROCESSING {
		t.Fatalf("open child: want telFAILED_PROCESSING, got %s", got)
	}
}

// TestXRPTransfer_InsufficientFundsGuard verifies the XRP-movement primitive
// refuses to underflow a sender whose balance is below the amount, returns the
// sentinel, and marks the funds-failure condition on the sandbox chain.
// Mirrors rippled accountSendXRP's sender-balance < amount branch.
func TestXRPTransfer_InsufficientFundsGuard(t *testing.T) {
	view := newPaymentMockLedgerView()
	var sender, receiver [20]byte
	sender[0] = 0x11
	receiver[0] = 0x22
	view.createAccount(sender, 100, 0)
	view.createAccount(receiver, 0, 0)

	sb := NewPaymentSandbox(view)

	// Sending more than the sender holds must trip the guard.
	err := xrpTransferInSandbox(sb, sender, receiver, 500)
	if err != errInsufficientFunds {
		t.Fatalf("over-balance transfer: want errInsufficientFunds, got %v", err)
	}
	if !sb.HasFundsFailure() {
		t.Fatalf("sandbox should record a funds failure after the guard trips")
	}

	// A transfer within balance must succeed and move funds.
	sbOK := NewPaymentSandbox(view)
	if err := xrpTransferInSandbox(sbOK, sender, receiver, 40); err != nil {
		t.Fatalf("within-balance transfer should succeed, got %v", err)
	}
	if sbOK.HasFundsFailure() {
		t.Fatalf("a successful transfer must not mark a funds failure")
	}
}

// TestFundsFailure_PropagatesToParent verifies a child sandbox that trips the
// guard surfaces the condition to the accumulating parent, so the flow result
// layer can observe it even when the child sandbox is later discarded.
func TestFundsFailure_PropagatesToParent(t *testing.T) {
	view := newPaymentMockLedgerView()
	var sender, receiver [20]byte
	sender[0] = 0x33
	receiver[0] = 0x44
	view.createAccount(sender, 10, 0)
	view.createAccount(receiver, 0, 0)

	parent := NewPaymentSandbox(view)
	child := NewChildSandbox(parent)

	if err := xrpTransferInSandbox(child, sender, receiver, 1000); err != errInsufficientFunds {
		t.Fatalf("want errInsufficientFunds, got %v", err)
	}
	// markFundsFailure walks the parent chain, so the parent sees it even
	// without an explicit Apply (the child may be a discarded dry strand).
	if !parent.HasFundsFailure() {
		t.Fatalf("parent sandbox should observe the child's funds failure")
	}
}
