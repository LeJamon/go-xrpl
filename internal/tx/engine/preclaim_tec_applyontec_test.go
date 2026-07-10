package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// preclaimTecApplierTx returns tecEXPIRED straight out of preclaim (doApply never
// runs) and implements TecApplier. Its ApplyOnTec bumps a marker account's owner
// count through the recovery view — a stand-in for the expired-credential
// deletion — so a test can prove the preclaim-tec commit path both invokes the
// hook and commits its writes.
type preclaimTecApplierTx struct {
	*txcore.BaseTx
	markerKey keylet.Keylet
	onTec     *bool
}

func (t preclaimTecApplierTx) Preclaim(txcore.LedgerView, txcore.EngineConfig) ter.Result {
	return ter.TecEXPIRED
}

func (t preclaimTecApplierTx) ApplyOnTec(ctx *txcore.ApplyContext) {
	*t.onTec = true
	data, err := ctx.View.Read(t.markerKey)
	if err != nil || data == nil {
		return
	}
	ar, err := state.ParseAccountRoot(data)
	if err != nil {
		return
	}
	ar.OwnerCount++
	out, err := state.SerializeAccountRoot(ar)
	if err != nil {
		return
	}
	_ = ctx.View.Update(t.markerKey, out)
}

// TestApply_PreclaimTecExpired_RunsApplyOnTec is the Theme-2 regression: a
// tecEXPIRED produced by preclaim (not doApply) must still run the transactor's
// ApplyOnTec hook, so its work-on-tec side effect persists — matching rippled,
// which routes every tecEXPIRED through the same reset + removeExpiredCredentials
// step regardless of whether the code came from preclaim or doApply. Before the
// fix, commitPreclaimTec charged the fee but never invoked ApplyOnTec, so the
// side effect vanished. The canonical case is expired-credential deletion (see
// internal/tx/credential and EscrowFinish's ApplyOnTec).
func TestApply_PreclaimTecExpired_RunsApplyOnTec(t *testing.T) {
	view := newRecordingBaseView()
	acctKey := fundRecoveryAccount(t, view, 1_000_000, 1)

	// A distinct marker account the hook mutates. Building its address from a
	// chosen id avoids depending on a hardcoded classic address.
	var markerID [20]byte
	markerID[0] = 0xAB
	markerAddr, err := state.EncodeAccountID(markerID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	markerKey := keylet.Account(markerID)
	markerData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  markerAddr,
		Balance:  500,
		Sequence: 1,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	if err := view.Insert(markerKey, markerData); err != nil {
		t.Fatalf("Insert marker: %v", err)
	}

	onTec := false
	txn := preclaimTecApplierTx{
		BaseTx:    recoveryTx(10, 1),
		markerKey: markerKey,
		onTec:     &onTec,
	}

	e := recoveryEngine(view, txcore.TapNONE)
	res := e.Apply(txn)

	if res.Result != ter.TecEXPIRED {
		t.Fatalf("result = %s, want tecEXPIRED", res.Result)
	}
	// tecEXPIRED is a work-on-tec code: applied, fee claimed, sequence consumed.
	if !res.Applied {
		t.Fatalf("tecEXPIRED must be applied (fee claimed)")
	}
	if !onTec {
		t.Fatalf("ApplyOnTec was not invoked from the preclaim-tec commit path")
	}

	// The hook's write must have committed through tecTable.Apply().
	marker := readRecoveryAccount(t, view, markerKey)
	if marker.OwnerCount != 1 {
		t.Fatalf("marker OwnerCount = %d, want 1 (ApplyOnTec write committed)", marker.OwnerCount)
	}

	// The payer still paid the fee and consumed its sequence.
	src := readRecoveryAccount(t, view, acctKey)
	if src.Balance != 999_990 {
		t.Fatalf("payer balance = %d, want 999990 (fee charged)", src.Balance)
	}
	if src.Sequence != 2 {
		t.Fatalf("payer sequence = %d, want 2 (consumed)", src.Sequence)
	}
}
