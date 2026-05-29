package tx

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/feetrack"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// newFeeTestTx builds a minimal AccountSet-typed transaction carrying the
// given Fee (drops, decimal string). AccountSet has no custom base-fee
// calculator, so preclaimBaseFee resolves to EngineConfig.BaseFee.
func newFeeTestTx(fee string) Transaction {
	t := NewBaseTx(TypeAccountSet, "rTestAccount")
	t.Fee = fee
	return t
}

// TestCheckFee_LoadFeeTrackScaling verifies that checkFee scales the
// open-ledger fee floor by the LoadFeeTrack factor (rippled's
// Transactor::minimumFee → scaleFeeLoad), and that the scaling is gated on
// OpenLedger and a non-nil tracker.
func TestCheckFee_LoadFeeTrackScaling(t *testing.T) {
	const baseFee = 10
	account := &state.AccountRoot{Balance: 1_000_000}

	// Remote fee at 2x the load base raises the effective floor to 2*baseFee.
	loaded := feetrack.New()
	loaded.SetRemoteFee(2 * feetrack.LoadBase)

	tests := []struct {
		name       string
		fee        string
		feeTrack   *feetrack.LoadFeeTrack
		openLedger bool
		want       Result
	}{
		{name: "nil tracker, fee meets base", fee: "10", feeTrack: nil, openLedger: true, want: TesSUCCESS},
		{name: "nil tracker, fee below base", fee: "9", feeTrack: nil, openLedger: true, want: TelINSUF_FEE_P},
		{name: "loaded, fee below scaled floor", fee: "10", feeTrack: loaded, openLedger: true, want: TelINSUF_FEE_P},
		{name: "loaded, fee meets scaled floor", fee: "20", feeTrack: loaded, openLedger: true, want: TesSUCCESS},
		{name: "loaded but ledger closed: no scaling", fee: "10", feeTrack: loaded, openLedger: false, want: TesSUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Engine{config: EngineConfig{
				BaseFee:    baseFee,
				OpenLedger: tt.openLedger,
				FeeTrack:   tt.feeTrack,
			}}
			txn := newFeeTestTx(tt.fee)
			if got := e.checkFee(txn, txn.GetCommon(), account); got != tt.want {
				t.Errorf("checkFee(fee=%s) = %v, want %v", tt.fee, got, tt.want)
			}
		})
	}
}

// TestCheckFee_UnlimitedCarveOut verifies that TapUNLIMITED is threaded into
// scaleFeeLoad: under moderate local-only load (between 1x and 4x remote) a
// privileged source pays the remote-rate floor instead of the local one,
// mirroring rippled's bUnlimited branch in scaleFeeLoad.
func TestCheckFee_UnlimitedCarveOut(t *testing.T) {
	const baseFee = 10
	account := &state.AccountRoot{Balance: 1_000_000}

	// Two raises lift the local fee to 320 (256 + 256/4) while remote and
	// cluster stay at the load base (256). feeFactor=320, remFee=256.
	tr := feetrack.New()
	tr.RaiseLocalFee() // first raise only arms the latch (raiseCount=1)
	tr.RaiseLocalFee() // second raise actually scales local up to 320

	// Non-unlimited floor: 10*320/256 = 12 (truncated). fee=10 is short.
	e := &Engine{config: EngineConfig{BaseFee: baseFee, OpenLedger: true, FeeTrack: tr}}
	txn := newFeeTestTx("10")
	if got := e.checkFee(txn, txn.GetCommon(), account); got != TelINSUF_FEE_P {
		t.Fatalf("checkFee(non-unlimited) = %v, want TelINSUF_FEE_P", got)
	}

	// Unlimited floor: carve-out drops feeFactor to remFee (256), so the
	// floor is 10*256/256 = 10 and fee=10 now suffices.
	eUnlimited := &Engine{config: EngineConfig{BaseFee: baseFee, OpenLedger: true, FeeTrack: tr, ApplyFlags: TapUNLIMITED}}
	if got := eUnlimited.checkFee(txn, txn.GetCommon(), account); got != TesSUCCESS {
		t.Fatalf("checkFee(unlimited) = %v, want TesSUCCESS", got)
	}
}
