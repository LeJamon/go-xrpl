package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type ingressRelayMemory struct {
	relayTestMemory
	onReserve func()
}

func (b *ingressRelayMemory) TryReserve(bytes uint64) bool {
	if b.onReserve != nil {
		b.onReserve()
	}
	return b.relayTestMemory.TryReserve(bytes)
}

func TestDetachedIngressRelayCachePrecedesSuccessorPromotion(t *testing.T) {
	for _, rpc := range []bool{false, true} {
		name := "peer"
		if rpc {
			name = "rpc"
		}
		t.Run(name, func(t *testing.T) {
			memory := &ingressRelayMemory{relayTestMemory: relayTestMemory{limit: relayCacheStructureBytes + 1<<20}}
			cfg := DefaultConfig()
			queueCfg := txq.StandaloneConfig()
			queueCfg.MinimumTxnInLedgerStandalone = 1
			queueCfg.TargetTxnInLedger = 1
			cfg.TxQ = &queueCfg
			cfg.RelayMemory = memory
			svc, err := New(cfg)
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)

			env := jtx.NewTestEnv(t)
			master, receiver := jtx.MasterAccount(), jtx.NewAccount("relay-promotion-recipient")
			var prior [][]byte
			for sequence := uint32(1); sequence <= 2; sequence++ {
				blob, _ := preferredSwitchPaymentBlob(t, env, master, receiver, 20_000_000, 10, sequence)
				outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, false)
				require.NoError(t, err)
				require.True(t, outcome.Applied)
				prior = append(prior, blob)
			}

			var gates []openLedgerGateSnapshot
			memory.onReserve = func() { gates = append(gates, svc.openLedgerMu.Snapshot()) }
			blob, hash := preferredSwitchPaymentBlob(t, env, master, receiver, 1_000_000, 10, 3)
			if rpc {
				transaction, err := tx.ParseFromBinary(blob)
				require.NoError(t, err)
				result, err := svc.SubmitTransaction(transaction, blob, false)
				require.NoError(t, err)
				require.Equal(t, ter.TerQUEUED, result.Result)
			} else {
				outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, false)
				require.NoError(t, err)
				require.True(t, outcome.Queued)
			}
			memory.onReserve = nil
			require.NotEmpty(t, gates)
			for _, gate := range gates {
				require.True(t, gate.Held, "successor publication must not overtake a queued relay-cache write")
				require.Equal(t, openLedgerIngress, gate.Owner)
			}
			_, _, deferred, ok := svc.relayCacheGet(hash)
			require.True(t, ok)
			require.True(t, deferred)

			parent := svc.GetClosedLedger()
			_, err = svc.AcceptConsensusResult(t.Context(), parent, prior, nil, parent.CloseTime().Add(time.Second), true)
			require.NoError(t, err)
			require.NoError(t, svc.waitSpeculativeSuccessor(t.Context()))
			got, included, deferred, ok := svc.TransactionForRelay(hash)
			require.True(t, ok)
			require.Equal(t, blob, got)
			require.True(t, included)
			require.False(t, deferred)
			require.Eventually(t, func() bool {
				_, _, deferred, ok := svc.relayCacheGet(hash)
				return ok && !deferred
			}, 5*time.Second, time.Millisecond)

			parent = svc.GetClosedLedger()
			_, err = svc.AcceptConsensusResult(t.Context(), parent, [][]byte{blob}, nil, parent.CloseTime().Add(time.Second), true)
			require.NoError(t, err)
			require.NoError(t, svc.waitSpeculativeSuccessor(t.Context()))
			exists, err := svc.GetClosedLedger().TxExists(hash)
			require.NoError(t, err)
			require.True(t, exists)
			_, queued := svc.txQueue.GetTxBlob(hash)
			require.False(t, queued)
			got, included, deferred, ok = svc.TransactionForRelay(hash)
			require.True(t, ok)
			require.Equal(t, blob, got)
			require.False(t, included)
			require.False(t, deferred, "closing a promoted transaction must retain its newer relay disposition")
		})
	}
}

type ownedIngressRun struct {
	outcome openledger.SubmitOutcome
	before  IngressMetricsSnapshot
	after   IngressMetricsSnapshot
	current *ledger.Ledger
	stored  []byte
}

func runOwnedIngress(t testing.TB, cfg Config, blob []byte, hash [32]byte, mutate func(*openledger.PendingTx)) ownedIngressRun {
	t.Helper()
	// Startup closes its initial ledger with wall time; comparisons need a fixed parent.
	env := jtx.NewTestEnvWithConfig(t, cfg.GenesisConfig)
	env.Close()
	parent := env.LastClosedLedger()
	db := newTestNodeStore(t, 1_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	writer := &Service{nodeStore: db}
	require.NoError(t, writer.persistToNodeStore(t.Context(), parent, parent.Sequence()))
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.Startup = StartupConfig{Mode: StartupLoad, Ledger: fmt.Sprintf("%X", parent.Hash())}
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.Equal(t, parent.Hash(), svc.GetClosedLedger().Hash())

	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)
	require.NoError(t, txengine.PrewarmSignatureWithRules(pending.Parsed, svc.TransactionRules()))
	if mutate != nil {
		mutate(&pending)
	}
	before := svc.OpenLedgerIngressMetrics()
	outcome, err := svc.SubmitOpenLedgerTxDetailedOwned(pending, true)
	require.NoError(t, err)
	require.True(t, outcome.Applied)
	after := svc.OpenLedgerIngressMetrics()
	current := svc.openLedgerView.Current()
	stored, found, err := current.GetTransaction(hash)
	require.NoError(t, err)
	require.True(t, found)
	return ownedIngressRun{
		outcome: outcome,
		before:  before,
		after:   after,
		current: current,
		stored:  stored,
	}
}

func ownedIngressPaymentBlob(t testing.TB, destinationName string, sequence uint32, lastLedgerSequence *uint32) ([]byte, [32]byte) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	destination := jtx.NewAccount(destinationName)
	transaction := payment.Pay(master, destination, 1_000_000).Sequence(sequence).Build()
	if lastLedgerSequence != nil {
		transaction.GetCommon().SetLastLedgerSequence(*lastLedgerSequence)
	}
	env.SignWith(transaction, master)
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	return blob, hash
}

func assertOwnedIngressMatchesBaseline(t testing.TB, baseline, got ownedIngressRun) {
	t.Helper()
	require.Equal(t, baseline.outcome, got.outcome)
	require.Equal(t, baseline.current.Hash(), got.current.Hash())
	require.Equal(t, baseline.current.Header().AccountHash, got.current.Header().AccountHash)
	require.Equal(t, baseline.current.Header().TxHash, got.current.Header().TxHash)
	require.Equal(t, baseline.current.TxCount(), got.current.TxCount())
	require.Equal(t, baseline.stored, got.stored)
}

func batchIngressConfig() Config {
	cfg := DefaultConfig()
	cfg.Startup.Mode = StartupFresh
	cfg.GenesisConfig = batchBenchmarkGenesisConfig()
	return cfg
}

func ownedIngressBatchBlob(t testing.TB) ([]byte, [32]byte) {
	t.Helper()
	env := jtx.NewTestEnvWithConfig(t, batchBenchmarkGenesisConfig())
	env.SetVerifySignatures(true)
	env.EnableFeatureNow("BatchV1_1")
	outer := jtx.MasterAccount()
	destination := jtx.NewAccount("owned-ingress-batch-destination")
	blob, hash, _ := buildServiceBatchBlob(t, env, outer, destination, 2, 1, 2, false)
	return blob, hash
}

func TestSubmitOpenLedgerTxOwnedReparsesMetadataMutations(t *testing.T) {
	lastLedgerSequence := uint32(100)
	cases := []struct {
		name   string
		blob   func(testing.TB) ([]byte, [32]byte)
		mutate func(*openledger.PendingTx)
	}{
		{
			name: "account",
			blob: func(tb testing.TB) ([]byte, [32]byte) {
				return ownedIngressPaymentBlob(tb, "owned-ingress-account", 1, nil)
			},
			mutate: func(pending *openledger.PendingTx) {
				pending.Account[0] ^= 1
			},
		},
		{
			name: "sequence",
			blob: func(tb testing.TB) ([]byte, [32]byte) {
				return ownedIngressPaymentBlob(tb, "owned-ingress-sequence", 1, nil)
			},
			mutate: func(pending *openledger.PendingTx) {
				pending.Sequence++
			},
		},
		{
			name: "is-ticket",
			blob: func(tb testing.TB) ([]byte, [32]byte) {
				return ownedIngressPaymentBlob(tb, "owned-ingress-ticket", 1, nil)
			},
			mutate: func(pending *openledger.PendingTx) {
				pending.IsTicket = true
			},
		},
		{
			name: "last-ledger-presence",
			blob: func(tb testing.TB) ([]byte, [32]byte) {
				return ownedIngressPaymentBlob(tb, "owned-ingress-last-presence", 1, nil)
			},
			mutate: func(pending *openledger.PendingTx) {
				pending.HasLastLedgerSequence = true
				pending.LastLedgerSequence = 1
			},
		},
		{
			name: "last-ledger-value",
			blob: func(tb testing.TB) ([]byte, [32]byte) {
				return ownedIngressPaymentBlob(tb, "owned-ingress-last-value", 1, &lastLedgerSequence)
			},
			mutate: func(pending *openledger.PendingTx) {
				pending.LastLedgerSequence++
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, hash := tc.blob(t)
			baseline := runOwnedIngress(t, DefaultConfig(), blob, hash, nil)
			mutated := runOwnedIngress(t, DefaultConfig(), blob, hash, tc.mutate)
			assertOwnedIngressMatchesBaseline(t, baseline, mutated)
			require.Equal(t, baseline.before.Reparses, baseline.after.Reparses)
			require.Equal(t, mutated.before.Reparses+1, mutated.after.Reparses)
		})
	}
}

func TestSubmitOpenLedgerTxOwnedReparsesMutatedPrewarmedBatchInner(t *testing.T) {
	blob, hash := ownedIngressBatchBlob(t)
	baseline := runOwnedIngress(t, batchIngressConfig(), blob, hash, nil)
	var originalInner tx.Transaction
	mutated := runOwnedIngress(t, batchIngressConfig(), blob, hash, func(pending *openledger.PendingTx) {
		outer, ok := pending.Parsed.(interface{ InnerTransactions() []tx.Transaction })
		require.True(t, ok)
		inners := outer.InnerTransactions()
		require.NotEmpty(t, inners)
		originalInner = inners[0]
		originalInner.GetCommon().Fee = "1"
	})
	assertOwnedIngressMatchesBaseline(t, baseline, mutated)
	require.Equal(t, "1", originalInner.GetCommon().Fee)
	require.Equal(t, baseline.before.Reparses, baseline.after.Reparses)
	require.Equal(t, mutated.before.Reparses+1, mutated.after.Reparses)
}

func TestDetachedIngressOwnedBatchStaleRetryReparsesAttemptAnnotations(t *testing.T) {
	blob, hash := ownedIngressBatchBlob(t)
	baseline := runOwnedIngress(t, batchIngressConfig(), blob, hash, nil)

	svc, err := New(batchIngressConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	releaseAdmission, err := svc.detachedIngress.acquire()
	require.NoError(t, err)
	defer releaseAdmission()
	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)
	require.NoError(t, txengine.PrewarmSignatureWithRules(pending.Parsed, svc.TransactionRules()))
	cold, family := successorPreparationParent(t, svc.GetClosedLedger(), jtx.MasterAccount().AccountID())
	defer family.unblock()
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	family.armed.Store(true)

	var attempts []tx.Transaction
	type ingressResult struct {
		outcome openledger.SubmitOutcome
		err     error
	}
	done := make(chan ingressResult, 1)
	go func() {
		outcome, _, _, _, submitErr := svc.submitDetachedIngressOwned(detachedIngressRequest{
			pendingTx:   pending,
			origin:      "peer",
			ownedParsed: true,
			prepareCommit: func(_ *ledger.Ledger, _ *ledger.Ledger, _ *txq.TxQ, _ openledger.ApplyConfig, _ openledger.SubmitOutcome, attempt openledger.PendingTx) detachedIngressCommitData {
				attempts = append(attempts, attempt.Parsed)
				if len(attempts) == 1 {
					if outer, ok := attempt.Parsed.(interface{ InnerTransactions() []tx.Transaction }); ok {
						inners := outer.InnerTransactions()
						if len(inners) > 0 {
							inners[0].GetCommon().Fee = "1"
						}
					}
				}
				return detachedIngressCommitData{}
			},
		})
		done <- ingressResult{outcome: outcome, err: submitErr}
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		family.unblock()
		t.Fatal("owned Batch ingress did not reach a blocked state read")
	}

	accepted := make(chan error, 1)
	go func() {
		_, acceptErr := svc.AcceptConsensusResult(t.Context(), cold, nil, nil, cold.CloseTime().Add(10*time.Second), true)
		accepted <- acceptErr
	}()
	select {
	case acceptErr := <-accepted:
		require.NoError(t, acceptErr)
	case <-time.After(time.Second):
		t.Fatal("consensus acceptance blocked behind owned Batch ingress")
	}
	family.unblock()
	result := <-done
	require.NoError(t, result.err)
	require.True(t, result.outcome.Applied)
	require.GreaterOrEqual(t, len(attempts), 2)
	require.NotSame(t, attempts[0], attempts[1])
	for _, attempt := range attempts[:2] {
		require.Equal(t, blob, attempt.GetRawBytes())
	}
	firstOuter, ok := attempts[0].(interface{ InnerTransactions() []tx.Transaction })
	require.True(t, ok)
	secondOuter, ok := attempts[1].(interface{ InnerTransactions() []tx.Transaction })
	require.True(t, ok)
	require.Equal(t, "1", firstOuter.InnerTransactions()[0].GetCommon().Fee)
	require.Equal(t, "0", secondOuter.InnerTransactions()[0].GetCommon().Fee)

	metrics := svc.OpenLedgerIngressMetrics()
	require.GreaterOrEqual(t, metrics.PrivateAttempts, uint64(2))
	require.GreaterOrEqual(t, metrics.StaleRetries, uint64(1))
	require.GreaterOrEqual(t, metrics.Reparses, uint64(1))
	stored, found, err := svc.openLedgerView.Current().GetTransaction(hash)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, baseline.stored, stored)
}

func TestDetachedIngressValidationAdvanceDoesNotRepeatApply(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	candidate := retainNextValidationCandidate(t, svc)
	blob, _ := startupPaymentBlob(t, "detached-validation", 1)
	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)
	release, err := svc.detachedIngress.acquire()
	require.NoError(t, err)
	defer release()
	attempts := 0
	outcome, _, _, _, err := svc.submitDetachedIngressOwned(detachedIngressRequest{
		pendingTx: pending,
		prepareCommit: func(*ledger.Ledger, *ledger.Ledger, *txq.TxQ, openledger.ApplyConfig, openledger.SubmitOutcome, openledger.PendingTx) detachedIngressCommitData {
			attempts++
			svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
			return detachedIngressCommitData{}
		},
	})
	require.NoError(t, err)
	require.True(t, outcome.Applied)
	require.Same(t, candidate, svc.GetValidatedLedger())
	require.Equal(t, 1, attempts, "validation alone must not replay an expensive private transaction")
}

func TestDetachedIngressDoesNotBlockConsensusAndPublishesRetry(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := successorPreparationParent(t, svc.GetClosedLedger(), jtx.MasterAccount().AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, hash := startupPaymentBlob(t, "detached-ingress-retry", 1)
	family.armed.Store(true)

	type ingressResult struct {
		outcome openledger.SubmitOutcome
		err     error
	}
	ingressDone := make(chan ingressResult, 1)
	go func() {
		outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		ingressDone <- ingressResult{outcome: outcome, err: err}
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach a blocked state read")
	}

	accepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), cold, nil, nil, cold.CloseTime().Add(10*time.Second), true)
		accepted <- err
	}()
	select {
	case err := <-accepted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("consensus acceptance blocked behind detached ingress")
	}

	family.unblock()
	result := <-ingressDone
	require.NoError(t, result.err)
	require.True(t, result.outcome.Applied)
	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.True(t, exists, "detached ingress outcome was lost after the frontier advanced")
	metrics := svc.OpenLedgerIngressMetrics()
	require.Greater(t, metrics.PrivateAttempts, uint64(1), "frontier advancement should execute a second private attempt")
	require.Greater(t, metrics.StaleRetries, uint64(0), "frontier advancement should be recorded as a stale retry")
	require.Greater(t, metrics.Reparses, uint64(0), "stale retry should reparse the canonical blob")
}

func TestDetachedIngressStopDrainsBlockedApply(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := successorPreparationParent(t, svc.GetClosedLedger(), jtx.MasterAccount().AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, _ := startupPaymentBlob(t, "detached-ingress-stop", 1)
	family.armed.Store(true)
	submitted := make(chan error, 1)
	go func() {
		_, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitted <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach a blocked state read")
	}

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	require.Eventually(t, func() bool {
		svc.lifecycleMu.Lock()
		defer svc.lifecycleMu.Unlock()
		return svc.lifecycleState == serviceStopping
	}, time.Second, time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("Stop returned while detached ingress was reading storage")
	default:
	}

	family.unblock()
	require.ErrorIs(t, <-submitted, errServiceNotRunning)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain detached ingress")
	}
}

func TestDetachedIngressRejectsReplayFaultAtFence(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	cold, family := successorPreparationParent(t, svc.GetClosedLedger(), jtx.MasterAccount().AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, hash := startupPaymentBlob(t, "detached-ingress-fault", 1)
	family.armed.Store(true)
	submitted := make(chan error, 1)
	go func() {
		_, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitted <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not reach a blocked state read")
	}
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))
	family.unblock()
	require.ErrorIs(t, <-submitted, replayfault.ErrBlocked)
	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.False(t, exists, "replay fault allowed detached ingress publication")
	require.Zero(t, svc.localTxs.Size(), "replay fault allowed local-pool side effects")
	_, _, _, cached := svc.relayCacheGet(hash)
	require.False(t, cached, "replay fault allowed relay-cache publication")
}

func TestDetachedIngressRejectsExistingReplayFault(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))

	blob, hash := startupPaymentBlob(t, "detached-ingress-blocked", 1)
	_, err = svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.ErrorIs(t, err, replayfault.ErrBlocked)
	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.False(t, exists)
	require.Zero(t, svc.localTxs.Size())
}

func TestDetachedIngressSnapshotCurrentRejectsReplayFaultWithoutGateLeak(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	snapshot, err := svc.captureDetachedIngress(detachedIngressRequest{})
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))

	current, err := svc.detachedIngressSnapshotCurrent(detachedIngressRequest{}, snapshot)
	require.ErrorIs(t, err, replayfault.ErrBlocked)
	require.False(t, current)
	require.True(t, svc.openLedgerMu.TryLock(), "snapshot fence leaked the open-ledger gate")
	svc.openLedgerMu.Unlock()
}

func TestDetachedIngressSnapshotCurrentRejectsStoppedService(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	snapshot, err := svc.captureDetachedIngress(detachedIngressRequest{})
	require.NoError(t, err)
	svc.lifecycleMu.Lock()
	previous := svc.lifecycleState
	svc.lifecycleState = serviceStopping
	svc.lifecycleMu.Unlock()
	defer func() {
		svc.lifecycleMu.Lock()
		svc.lifecycleState = previous
		svc.lifecycleMu.Unlock()
	}()

	current, err := svc.detachedIngressSnapshotCurrent(detachedIngressRequest{}, snapshot)
	require.ErrorIs(t, err, errServiceNotRunning)
	require.False(t, current)
	require.True(t, svc.openLedgerMu.TryLock(), "stopped snapshot fence leaked the open-ledger gate")
	svc.openLedgerMu.Unlock()
}

func TestDetachedIngressRejectsExhaustedMemory(t *testing.T) {
	budget := &preferredMemoryBudget{
		limit: 8 << 20, capacity: 8 << 20, used: 8 << 20, changed: make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	blob, _ := startupPaymentBlob(t, "detached-ingress-memory", 1)
	submitted := make(chan error, 1)
	go func() {
		_, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
		submitted <- err
	}()
	select {
	case err := <-submitted:
		require.ErrorIs(t, err, ErrPreferredTransitionMemory)
	case <-time.After(time.Second):
		t.Fatal("ingress waited for credits retained by unfinished work")
	}
	svc.Stop()
	require.Equal(t, uint64(8<<20), budget.Used())
	budget.Release(8 << 20)
	require.Zero(t, budget.Used())
}

func TestDetachedIngressAdmissionBoundsWaitersAndStops(t *testing.T) {
	var admission detachedIngressAdmission
	releaseActive, err := admission.acquire()
	require.NoError(t, err)

	results := make(chan error, detachedIngressWaitLimit)
	for range detachedIngressWaitLimit {
		go func() {
			release, err := admission.acquire()
			if err == nil {
				release()
			}
			results <- err
		}()
	}
	require.Eventually(t, func() bool {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		return len(admission.waiters) == detachedIngressWaitLimit
	}, time.Second, time.Millisecond)

	_, err = admission.acquire()
	require.ErrorIs(t, err, errDetachedIngressBusy)

	done := admission.stop()
	select {
	case <-done:
		t.Fatal("active detached ingress completed before its owner released")
	default:
	}
	for range detachedIngressWaitLimit {
		require.ErrorIs(t, <-results, errServiceNotRunning)
	}

	releaseActive()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("detached ingress admission did not drain")
	}

	_, err = admission.acquire()
	require.ErrorIs(t, err, errServiceNotRunning)
}

func TestDetachedIngressAdmissionStopDrainsHandedOffOwner(t *testing.T) {
	var admission detachedIngressAdmission
	releaseActive, err := admission.acquire()
	require.NoError(t, err)

	ready := make(chan func(), 1)
	go func() {
		release, err := admission.acquire()
		if err != nil {
			ready <- nil
			return
		}
		ready <- release
	}()
	require.Eventually(t, func() bool {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		return len(admission.waiters) == 1
	}, time.Second, time.Millisecond)

	releaseActive()
	releaseOwner := <-ready
	require.NotNil(t, releaseOwner)
	done := admission.stop()
	select {
	case <-done:
		t.Fatal("stop completed while the handed-off owner was active")
	default:
	}
	releaseOwner()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not wait for the handed-off owner")
	}
}
