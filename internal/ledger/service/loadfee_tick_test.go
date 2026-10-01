package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/feetrack"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

func TestConsensusLoadFeeUsesPublishedSuccessor(t *testing.T) {
	for _, includeTransactions := range []bool{false, true} {
		name := "retained transactions raise load"
		if includeTransactions {
			name = "closed transactions lower load"
		}
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			queueCfg := txq.StandaloneConfig()
			queueCfg.MinimumTxnInLedger = 1
			queueCfg.MinimumTxnInLedgerStandalone = 1
			queueCfg.TargetTxnInLedger = 1
			queueCfg.MaximumTxnInLedger = 1
			cfg.TxQ = &queueCfg
			svc, err := New(cfg)
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)

			env := jtx.NewTestEnv(t)
			master, receiver := jtx.MasterAccount(), jtx.NewAccount("load-fee-recipient")
			var blobs [][]byte
			for sequence := uint32(1); sequence <= 2; sequence++ {
				blob, _ := preferredSwitchPaymentBlob(t, env, master, receiver, 20_000_000, 10, sequence)
				result, err := svc.SubmitOpenLedgerTxDetailed(blob, false)
				require.NoError(t, err)
				require.Equal(t, ter.TesSUCCESS, result.Result)
				require.True(t, result.Applied)
				blobs = append(blobs, blob)
			}
			require.Zero(t, svc.openLedger.TxCount())
			require.Equal(t, uint32(2), svc.GetOpenLedger().TxCount())
			require.Greater(t, svc.TxQMetrics().OpenLedgerFeeLevel, svc.TxQMetrics().ReferenceFeeLevel)

			svc.feeTrack.RaiseLocalFee()
			if includeTransactions {
				svc.feeTrack.RaiseLocalFee()
			} else {
				blobs = nil
			}
			before := svc.feeTrack.LocalFee()
			parent := svc.GetClosedLedger()
			_, err = svc.AcceptConsensusResult(t.Context(), parent, blobs, nil, parent.CloseTime().Add(time.Second), true)
			require.NoError(t, err)

			if includeTransactions {
				require.Equal(t, uint32(2), svc.GetClosedLedger().TxCount())
				require.Zero(t, svc.GetOpenLedger().TxCount())
				require.Less(t, svc.feeTrack.LocalFee(), before)
			} else {
				require.Zero(t, svc.GetClosedLedger().TxCount())
				require.Equal(t, uint32(2), svc.GetOpenLedger().TxCount())
				require.Greater(t, svc.feeTrack.LocalFee(), before)
			}
		})
	}
}

// TestTickLoadFee_NoOverload_LowersToLoadBase pins the rippled
// LoadManager.cpp:177-186 lower-branch: when the overload signal is
// false the local fee decays back to LoadBase.
func TestTickLoadFee_NoOverload_LowersToLoadBase(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Pre-load the tracker: two raises lift local fee above LoadBase.
	ft := svc.feeTrack
	ft.RaiseLocalFee()
	ft.RaiseLocalFee()
	if got := ft.LocalFee(); got <= feetrack.LoadBase {
		t.Fatalf("setup: pre-tick local fee = %d; want > LoadBase", got)
	}

	// Fresh open ledger has no txs → TxQ feeEscalation collapses to
	// reference, so the tick fires the lower branch on every call. A
	// fixed number of ticks must drive the fee back to LoadBase.
	svc.mu.Lock()
	for range 80 {
		svc.tickLoadFeeLocked()
	}
	svc.mu.Unlock()

	if got := ft.LocalFee(); got != feetrack.LoadBase {
		t.Fatalf("post-tick local fee = %d; want LoadBase=%d", got, feetrack.LoadBase)
	}
}

// TestTickLoadFee_NilTracker is a defensive no-op check: a Service
// without a tracker (legacy/test fixture, or any future codepath that
// nils it out) must not panic when the tick runs.
func TestTickLoadFee_NilTracker(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc.feeTrack = nil

	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.tickLoadFeeLocked() // must not panic
}
