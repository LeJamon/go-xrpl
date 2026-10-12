package service

import (
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

// This workload measures service switching and competing reads/submission. Engine
// heartbeat and validation outcomes require the recovery integration tests.
func BenchmarkService_PreferredSwitchBatch(b *testing.B) {
	for _, count := range []int{8, 32} {
		b.Run(fmt.Sprintf("batches-%d", count), func(b *testing.B) {
			benchmarkPreferredSwitchBatch(b, count)
		})
	}
}

func benchmarkPreferredSwitchBatch(b *testing.B, count int) {
	b.StopTimer()
	env := jtx.NewTestEnv(b)
	env.EnableFeatureNow("BatchV1_1")
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	retrying := make([]*jtx.Account, count)
	for i := range retrying {
		retrying[i] = jtx.NewAccount(fmt.Sprintf("preferred-benchmark-retrying-%d", i))
	}
	receiver := jtx.NewAccount("preferred-benchmark-receiver")
	ingress := jtx.NewAccount("preferred-benchmark-ingress")
	const innerCount = 8
	makeBatches := func(senders []*jtx.Account, stride int) []openledger.PendingTx {
		pending := make([]openledger.PendingTx, count)
		for i := range pending {
			sender := senders[i%len(senders)]
			batch := batchtx.NewBatch(sender.Address)
			seq := uint32(1 + i*stride)
			batch.SetSequence(seq)
			batch.Fee = "100000"
			batch.SetFlags(batchtx.BatchFlagAllOrNothing)
			for j := range innerCount {
				inner := payment.Pay(sender, receiver, 1).Fee(0).Sequence(seq + uint32(j) + 1).Build()
				inner.GetCommon().SigningPubKey = ""
				inner.GetCommon().SetFlags(tx.TfInnerBatchTxn)
				batch.AddInnerTransaction(inner)
			}
			env.SignWith(batch, sender)
			fields, err := batch.Flatten()
			require.NoError(b, err)
			encoded, err := binarycodec.Encode(fields)
			require.NoError(b, err)
			blob, err := hex.DecodeString(encoded)
			require.NoError(b, err)
			pending[i], err = openledger.ParsePendingTx(blob)
			require.NoError(b, err)
		}
		return pending
	}
	closedTxs, retryTxs := makeBatches([]*jtx.Account{master}, innerCount+1), makeBatches(retrying, 0)
	salt, err := openledger.ComputeSalt(closedTxs)
	require.NoError(b, err)
	ingressBlob, ingressHash := preferredSwitchPaymentBlob(b, env, ingress, receiver, 1, 100000, 1)
	switches := make([]time.Duration, 0, b.N)
	readWaits := make([]time.Duration, 0, b.N)
	gateHolds := make([]time.Duration, 0, b.N)
	ingressWaits := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		cfg := DefaultConfig()
		cfg.Startup.Mode = StartupFresh
		cfg.GenesisConfig.Amendments = append(cfg.GenesisConfig.Amendments, amendment.FeatureBatchV1_1)
		for _, account := range append([]*jtx.Account{receiver, ingress}, retrying...) {
			cfg.GenesisConfig.InitialAccounts = append(cfg.GenesisConfig.InitialAccounts, genesis.InitialAccount{
				Address: account.Address, Balance: 1_000_000_000, Sequence: 1,
			})
		}
		queue := txq.StandaloneConfig()
		queue.MinimumTxnInLedgerStandalone = 100000
		queue.TargetTxnInLedger = 100000
		cfg.TxQ = &queue
		svc, err := New(cfg)
		require.NoError(b, err)
		require.NoError(b, svc.Start())
		require.True(b, svc.TransactionRules().Enabled(amendment.FeatureBatchV1_1))
		parent := svc.GetClosedLedger()
		preferred, retries, err := svc.buildClosedLedger(b.Context(), parent, closedTxs, salt, parent.CloseTime().Add(time.Second), false, nil)
		require.NoError(b, err)
		require.Empty(b, retries)
		require.Equal(b, uint32(count*(innerCount+1)), preferred.TxCount())
		require.NoError(b, preferred.Close(parent.CloseTime().Add(time.Second), 0))
		for _, pending := range retryTxs {
			outcome, err := svc.SubmitOpenLedgerTxDetailed(pending.Blob, true)
			require.NoError(b, err)
			require.Equal(b, ter.TesSUCCESS, outcome.Result)
			require.True(b, outcome.Applied)
		}

		stop := make(chan struct{})
		started := make(chan struct{})
		switchStarted := make(chan struct{})
		var observers sync.WaitGroup
		var maxReadWait, maxGateHold time.Duration
		observers.Add(2)
		go func() {
			defer observers.Done()
			close(started)
			for {
				select {
				case <-stop:
					return
				default:
				}
				start := time.Now()
				_ = svc.GetClosedLedger()
				_, _ = svc.GetLedgerBySequence(parent.Sequence())
				if elapsed := time.Since(start); elapsed > maxReadWait {
					maxReadWait = elapsed
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
		go func() {
			defer observers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				gate := svc.openLedgerMu.Snapshot()
				if gate.HeldFor > maxGateHold {
					maxGateHold = gate.HeldFor
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
		type submission struct {
			wait    time.Duration
			applied bool
			err     error
		}
		submitted := make(chan submission, 1)
		go func() {
			<-switchStarted
			time.Sleep(time.Millisecond)
			start := time.Now()
			outcome, submitErr := svc.SubmitOpenLedgerTxDetailed(ingressBlob, true)
			submitted <- submission{wait: time.Since(start), applied: outcome.Applied, err: submitErr}
		}()
		<-started
		b.StartTimer()
		start := time.Now()
		err = svc.switchToPreferredLedger(preferred, func() { close(switchStarted) })
		switches = append(switches, time.Since(start))
		b.StopTimer()
		close(stop)
		observers.Wait()
		require.NoError(b, err)
		readWaits = append(readWaits, maxReadWait)
		gateHolds = append(gateHolds, maxGateHold)
		submitResult := <-submitted
		ingressWaits = append(ingressWaits, submitResult.wait)
		require.NoError(b, submitResult.err)
		require.True(b, submitResult.applied)
		current := svc.openLedgerView.Current()
		for _, hash := range append([][32]byte{ingressHash}, retryTxs[0].Hash, retryTxs[len(retryTxs)-1].Hash) {
			present, err := current.TxExists(hash)
			require.NoError(b, err)
			require.True(b, present)
		}
		svc.Stop()
	}
	reportConsensusAcceptanceQuantiles(b, "switch", switches)
	reportConsensusAcceptanceQuantiles(b, "reader-worst", readWaits)
	reportConsensusAcceptanceQuantiles(b, "sampled-gate-hold", gateHolds)
	reportConsensusAcceptanceQuantiles(b, "concurrent-submit", ingressWaits)
	b.ReportMetric(float64(count*innerCount), "inner-txs/set")
}
