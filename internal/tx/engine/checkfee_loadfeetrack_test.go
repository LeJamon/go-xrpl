package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// newFeeTestTx builds a minimal AccountSet-typed transaction carrying the
// given Fee (drops, decimal string). AccountSet has no custom base-fee
// calculator, so preclaimBaseFee resolves to EngineConfig.BaseFee.
func newFeeTestTx(fee string) txcore.Transaction {
	t := txcore.NewBaseTx(txcore.TypeAccountSet, "rTestAccount")
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
		want       ter.Result
	}{
		{name: "nil tracker, fee meets base", fee: "10", feeTrack: nil, openLedger: true, want: ter.TesSUCCESS},
		{name: "nil tracker, fee below base", fee: "9", feeTrack: nil, openLedger: true, want: ter.TelINSUF_FEE_P},
		{name: "loaded, fee below scaled floor", fee: "10", feeTrack: loaded, openLedger: true, want: ter.TelINSUF_FEE_P},
		{name: "loaded, fee meets scaled floor", fee: "20", feeTrack: loaded, openLedger: true, want: ter.TesSUCCESS},
		{name: "loaded but ledger closed: no scaling", fee: "10", feeTrack: loaded, openLedger: false, want: ter.TesSUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Engine{config: txcore.EngineConfig{
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
	e := &Engine{config: txcore.EngineConfig{BaseFee: baseFee, OpenLedger: true, FeeTrack: tr}}
	txn := newFeeTestTx("10")
	if got := e.checkFee(txn, txn.GetCommon(), account); got != ter.TelINSUF_FEE_P {
		t.Fatalf("checkFee(non-unlimited) = %v, want TelINSUF_FEE_P", got)
	}

	// Unlimited floor: carve-out drops feeFactor to remFee (256), so the
	// floor is 10*256/256 = 10 and fee=10 now suffices.
	eUnlimited := &Engine{config: txcore.EngineConfig{BaseFee: baseFee, OpenLedger: true, FeeTrack: tr, ApplyFlags: txcore.TapUNLIMITED}}
	if got := eUnlimited.checkFee(txn, txn.GetCommon(), account); got != ter.TesSUCCESS {
		t.Fatalf("checkFee(unlimited) = %v, want TesSUCCESS", got)
	}
}

// TestCheckFee_EnforceLoadFee covers the EnforceLoadFee gate used by the TxQ
// direct-apply / clear-queue / accept paths (OpenLedger=false). The floor must
// fire only while load is elevated, mirroring rippled's open-view floor under
// load, and stay inert at normal load and on genuinely closed-ledger applies.
func TestCheckFee_EnforceLoadFee(t *testing.T) {
	const baseFee = 10
	account := &state.AccountRoot{Balance: 1_000_000}

	// Remote fee at 2x base raises the effective floor to 2*baseFee = 20.
	loaded := feetrack.New()
	loaded.SetRemoteFee(2 * feetrack.LoadBase)

	tests := []struct {
		name     string
		fee      string
		feeTrack *feetrack.LoadFeeTrack
		enforce  bool
		want     ter.Result
	}{
		{name: "enforce, elevated load, fee below scaled floor", fee: "10", feeTrack: loaded, enforce: true, want: ter.TelINSUF_FEE_P},
		{name: "enforce, elevated load, fee meets scaled floor", fee: "20", feeTrack: loaded, enforce: true, want: ter.TesSUCCESS},
		{name: "enforce, normal load: floor inert (admission covers base)", fee: "5", feeTrack: feetrack.New(), enforce: true, want: ter.TesSUCCESS},
		{name: "enforce, nil tracker: floor inert", fee: "5", feeTrack: nil, enforce: true, want: ter.TesSUCCESS},
		{name: "no enforce, elevated load (closed apply): never scales", fee: "10", feeTrack: loaded, enforce: false, want: ter.TesSUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Engine{config: txcore.EngineConfig{
				BaseFee:        baseFee,
				OpenLedger:     false,
				EnforceLoadFee: tt.enforce,
				FeeTrack:       tt.feeTrack,
			}}
			txn := newFeeTestTx(tt.fee)
			if got := e.checkFee(txn, txn.GetCommon(), account); got != tt.want {
				t.Errorf("checkFee(fee=%s) = %v, want %v", tt.fee, got, tt.want)
			}
		})
	}
}

// TestCheckFee_InsufficientBalance covers the balance-below-fee branch:
// deterministic tecINSUFF_FEE only when balance>0 AND the view is closed, else
// the retryable terINSUF_FEE_B. The view counts as open for OpenLedger, the TxQ
// paths (EnforceLoadFee), or the open-ledger submit path (ViewOpen) — not just
// OpenLedger. Reference: rippled Transactor::checkFee.
func TestCheckFee_InsufficientBalance(t *testing.T) {
	const baseFee = 10

	tests := []struct {
		name           string
		balance        uint64
		openLedger     bool
		enforceLoadFee bool
		viewOpen       bool
		want           ter.Result
	}{
		{name: "closed ledger, non-zero balance below fee", balance: 50, openLedger: false, want: ter.TecINSUFF_FEE},
		{name: "closed ledger, zero balance", balance: 0, openLedger: false, want: ter.TerINSUF_FEE_B},
		{name: "open ledger, non-zero balance below fee", balance: 50, openLedger: true, want: ter.TerINSUF_FEE_B},
		{name: "open ledger, zero balance", balance: 0, openLedger: true, want: ter.TerINSUF_FEE_B},
		{name: "open view via EnforceLoadFee (TxQ apply/accept), non-zero balance below fee", balance: 50, enforceLoadFee: true, want: ter.TerINSUF_FEE_B},
		{name: "open view via ViewOpen (open-ledger submit), non-zero balance below fee", balance: 50, viewOpen: true, want: ter.TerINSUF_FEE_B},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Engine{config: txcore.EngineConfig{
				BaseFee:        baseFee,
				OpenLedger:     tt.openLedger,
				EnforceLoadFee: tt.enforceLoadFee,
				ViewOpen:       tt.viewOpen,
			}}
			account := &state.AccountRoot{Balance: tt.balance}
			// Fee of 100 drops exceeds both balances yet clears the open-ledger
			// base-fee floor, so the balance branch is the one under test.
			txn := newFeeTestTx("100")
			if got := e.checkFee(txn, txn.GetCommon(), account); got != tt.want {
				t.Errorf("checkFee(balance=%d, viewOpen=%v) = %v, want %v", tt.balance, e.config.IsViewOpen(), got, tt.want)
			}
		})
	}
}
