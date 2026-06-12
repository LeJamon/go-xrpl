package tx

import "testing"

// TestTelFailedProcessing_LocalHoldClassification locks the submit-path
// contract the open-ledger FAILED_PROCESSING guard relies on: a
// telFAILED_PROCESSING result is a local error that is NOT applied, so the
// submit path holds it locally (retriable, kept) without claiming a fee or
// relaying it to peers.
//
// The relay/keep decision in LedgerServiceAdapter.submitTransaction and the
// localTxs push in service.SubmitTransaction both branch on result.Applied:
// relay and fee-claim happen only when Applied is true, while a non-fail_hard
// submission is still held. tel is never applied, so it is held but neither
// relayed nor charged — matching rippled NetworkOPs.cpp:1674-1689.
func TestTelFailedProcessing_LocalHoldClassification(t *testing.T) {
	r := TelFAILED_PROCESSING

	if r.IsApplied() {
		t.Fatalf("telFAILED_PROCESSING must not be applied (no fee claimed, not relayed)")
	}
	if !r.IsTel() {
		t.Fatalf("telFAILED_PROCESSING must classify as a tel (local error) code")
	}
	if r.IsTec() {
		t.Fatalf("telFAILED_PROCESSING must not classify as a tec (no fee claim)")
	}

	// The closed-view variant is a tec: it claims a fee and is applied, the
	// behaviour consensus apply must keep producing on the closed-view path.
	c := TecFAILED_PROCESSING
	if !c.IsTec() || !c.IsApplied() {
		t.Fatalf("tecFAILED_PROCESSING must claim a fee and be applied on the closed-view path")
	}
}
