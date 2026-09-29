package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestApplyThreadingAndFeeDestructionAtClose(t *testing.T) {
	for _, mode := range []struct {
		name      string
		open      bool
		configure func(*txcore.EngineConfig)
	}{
		{"closed", false, func(*txcore.EngineConfig) {}},
		{"open", true, func(c *txcore.EngineConfig) { c.OpenLedger = true }},
		{"txq", true, func(c *txcore.EngineConfig) { c.EnforceLoadFee = true }},
		{"replay", true, func(c *txcore.EngineConfig) { c.ViewOpen = true }},
	} {
		for _, result := range []ter.Result{ter.TesSUCCESS, ter.TecUNFUNDED_PAYMENT} {
			for _, dryRun := range []bool{false, true} {
				name := mode.name + "/" + result.String()
				if dryRun {
					name += "/dry-run"
				}
				t.Run(name, func(t *testing.T) {
					view := newRecordingBaseView()
					key := fundRecoveryAccount(t, view, 1_000_000, 1)
					account := readRecoveryAccount(t, view, key)
					previous := [32]byte{1, 2, 3}
					account.PreviousTxnID = previous
					account.PreviousTxnLgrSeq = 3
					data, err := state.SerializeAccountRoot(account)
					if err != nil {
						t.Fatal(err)
					}
					if err := view.Update(key, data); err != nil {
						t.Fatal(err)
					}
					engine := recoveryEngine(view, txcore.TapNONE)
					mode.configure(&engine.config)
					if dryRun {
						engine.config.ApplyFlags |= txcore.TapDRY_RUN
					}
					transaction := codeTecTx{BaseTx: recoveryTx(10, 1), code: result}
					out := engine.Apply(transaction)
					if out.Result != result || out.Applied == dryRun || out.Fee != 10 {
						t.Fatalf("result/applied/fee = %s/%v/%d", out.Result, out.Applied, out.Fee)
					}
					account = readRecoveryAccount(t, view, key)
					if mode.open || dryRun {
						if account.PreviousTxnID != previous || account.PreviousTxnLgrSeq != 3 || view.destroyed != 0 {
							t.Fatalf("threading or supply changed before close: account=%+v destroyed=%d", account, view.destroyed)
						}
					} else {
						hash, err := txcore.ComputeTransactionHash(transaction)
						if err != nil {
							t.Fatal(err)
						}
						if account.PreviousTxnID != hash || account.PreviousTxnLgrSeq != 100 || view.destroyed != drops.XRPAmount(10) {
							t.Fatalf("closed threading/supply mismatch: account=%+v destroyed=%d", account, view.destroyed)
						}
					}
					balance, sequence := uint64(999_990), uint32(2)
					if dryRun {
						balance, sequence = 1_000_000, 1
					}
					if account.Balance != balance || account.Sequence != sequence {
						t.Fatalf("balance/sequence = %d/%d, want %d/%d", account.Balance, account.Sequence, balance, sequence)
					}
					if out.Metadata == nil || len(out.Metadata.AffectedNodes) == 0 {
						t.Fatal("missing diagnostic metadata")
					}
				})
			}
		}
	}
}
