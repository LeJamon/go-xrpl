package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestIngressPrefetchDoesNotHoldGateAndRechecksCurrentLedger(t *testing.T) {
	for _, rpc := range []bool{false, true} {
		name := "network"
		if rpc {
			name = "rpc"
		}
		t.Run(name, func(t *testing.T) {
			db := newTestNodeStore(t, 10000)
			cfg := DefaultConfig()
			cfg.NodeStore, cfg.SHAMapFamily = db, backend.New(db)
			svc, err := New(cfg)
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(func() { svc.Stop(); require.NoError(t, db.Close()) })
			warm := svc.GetClosedLedger()
			preferred, err := ledger.NewOpen(warm, warm.CloseTime().Add(20*time.Second))
			require.NoError(t, err)
			require.NoError(t, preferred.Close(warm.CloseTime().Add(20*time.Second), 0))
			cold, err := ledger.NewOpen(warm, warm.CloseTime().Add(10*time.Second))
			require.NoError(t, err)
			require.NoError(t, cold.Close(warm.CloseTime().Add(10*time.Second), 0))
			stateMap, err := cold.StateMapSnapshot()
			require.NoError(t, err)
			root, err := stateMap.Hash()
			require.NoError(t, err)
			// The source is already backed and clean: reopen from its actual
			// NodeStore instead of copying only dirty nodes into another DB.
			family := &acceptanceBlockingFamily{Family: cfg.SHAMapFamily, entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(family.unblock)
			coldState, err := shamap.NewFromRootHash(shamap.TypeState, root, family)
			require.NoError(t, err)
			cold, err = ledger.NewFromHeader(cold.Header(), coldState, shamap.New(shamap.TypeTransaction), cold.Fees())
			require.NoError(t, err)
			require.NoError(t, svc.SwitchToPreferredLedger(cold))
			svc.mu.RLock()
			_, err = svc.applyConfigLocked()
			svc.mu.RUnlock()
			require.NoError(t, err)
			blob, hash := startupPaymentBlob(t, "off-gate-prefetch", 1)
			family.armed.Store(true)
			type result struct {
				applied bool
				code    ter.Result
				err     error
			}
			done := make(chan result, 1)
			go func() {
				if rpc {
					parsed, err := tx.ParseFromBinary(blob)
					if err != nil {
						done <- result{err: err}
						return
					}
					out, err := svc.SubmitTransaction(parsed, blob, false)
					if out == nil {
						done <- result{err: err}
						return
					}
					// The one-XRP fixture can claim a fee with
					// tecNO_DST_INSUF_XRP under the genesis reserve. Like the
					// network route, test committed application, not payment success.
					done <- result{applied: out.Applied && (out.Result == ter.TesSUCCESS || out.Result.IsTec()), code: out.Result, err: err}
					return
				}
				out, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
				done <- result{applied: out.Applied && out.Class == openledger.ResultSuccess, code: out.Result, err: err}
			}()
			select {
			case <-family.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("prefetch did not reach cold state")
			}
			require.False(t, svc.openLedgerMu.Snapshot().Held, "cold prefetch must not own the ingress gate")
			switched := make(chan error, 1)
			go func() { switched <- svc.SwitchToPreferredLedger(preferred) }()
			select {
			case err := <-switched:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("prefetch retained a live-view lock needed by ledger replacement")
			}
			family.unblock()
			select {
			case out := <-done:
				require.NoError(t, out.err)
				require.True(t, out.applied, "submit result %s", out.code)
			case <-time.After(3 * time.Second):
				t.Fatal("submission did not resume after prefetch")
			}
			require.Equal(t, preferred.Hash(), svc.GetClosedLedger().Hash())
			exists, err := svc.openLedgerView.Current().TxExists(hash)
			require.NoError(t, err)
			require.True(t, exists, "submission was applied to a discarded prefetch view")
		})
	}
}
