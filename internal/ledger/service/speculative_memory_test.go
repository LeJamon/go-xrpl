package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

const issue2071MiB = uint64(1 << 20)

type issue2071Budget struct {
	pool        *membudget.Pool
	persistence *membudget.Account
	speculative *membudget.Account
}

func newIssue2071Budget(t testing.TB, persistenceMinimum, persistenceMaximum, speculativeMaximum, poolExtra uint64) issue2071Budget {
	t.Helper()
	poolLimit := persistenceMaximum + speculativeMaximum + poolExtra
	pool := membudget.New(poolLimit)
	persistence, err := pool.NewAccount("persistence", persistenceMinimum, persistenceMaximum)
	require.NoError(t, err)
	speculative, err := pool.NewAccount("speculative", 0, speculativeMaximum)
	require.NoError(t, err)
	return issue2071Budget{pool: pool, persistence: persistence, speculative: speculative}
}

func newIssue2071Service(t testing.TB, budget issue2071Budget) *Service {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget.persistence
	cfg.SpeculativeMemory = budget.speculative
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	return svc
}

func issue2071SignedPayment(t testing.TB, env *jtx.TestEnv, sender, receiver *jtx.Account, sequence uint32) ([]byte, [32]byte) {
	return issue2071SignedPaymentAmount(t, env, sender, receiver, 1_000_000, sequence)
}

func issue2071SignedPaymentAmount(t testing.TB, env *jtx.TestEnv, sender, receiver *jtx.Account, amount uint64, sequence uint32) ([]byte, [32]byte) {
	t.Helper()
	env.SetVerifySignatures(true)
	transaction := payment.Pay(sender, receiver, amount).Sequence(sequence).Build()
	env.SignWith(transaction, sender)
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	return blob, hash
}

func issue2071FundedPayment(t testing.TB, svc *Service, env *jtx.TestEnv, name string) ([]byte, [32]byte) {
	t.Helper()
	receiver := jtx.NewAccount(name)
	funding, _ := issue2071SignedPaymentAmount(t, env, jtx.MasterAccount(), receiver, 100_000_000, 1)
	outcome, err := svc.SubmitOpenLedgerTxDetailed(funding, false)
	require.NoError(t, err)
	require.True(t, outcome.Applied, "account funding returned %s", outcome.Result)
	return issue2071SignedPayment(t, env, jtx.MasterAccount(), receiver, 2)
}

func issue2071ClosedChild(t testing.TB, parent *ledger.Ledger) *ledger.Ledger {
	t.Helper()
	closeTime := parent.CloseTime().Add(time.Second)
	child, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, child.Close(closeTime, 0))
	return child
}

func issue2071WaitForDenied(t testing.TB, account *membudget.Account) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if account.Snapshot().Denied != 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("memory admission did not reach a denied reservation")
		case <-ticker.C:
		}
	}
}

func issue2071RequireGateFree(t testing.TB, svc *Service) {
	t.Helper()
	require.Eventually(t, func() bool {
		if svc.preferredTransitionIsActive() {
			return false
		}
		if !svc.openLedgerMu.TryLock() {
			return false
		}
		svc.openLedgerMu.Unlock()
		return true
	}, time.Second, time.Millisecond)
}

func issue2071OpenHashes(t testing.TB, svc *Service) ([32]byte, [32]byte) {
	t.Helper()
	open := svc.GetOpenLedger()
	stateHash, err := open.StateMapHash()
	require.NoError(t, err)
	txHash, err := open.TxMapHash()
	require.NoError(t, err)
	return stateHash, txHash
}

func TestIssue2071SpeculativeMemoryIsolationAcceptsEveryIngressOrigin(t *testing.T) {
	for _, origin := range []string{"peer", "local", "rpc", "txset"} {
		t.Run(origin, func(t *testing.T) {
			budget := newIssue2071Budget(t, 256<<20, 256<<20, 256<<20, 0)
			svc := newIssue2071Service(t, budget)

			env := jtx.NewTestEnv(t)
			blob, hash := issue2071FundedPayment(t, svc, env, "issue-2071-"+origin)
			require.True(t, budget.persistence.TryReserve(256<<20))
			defer budget.persistence.Release(256 << 20)
			var outcome openledger.SubmitOutcome
			switch origin {
			case "peer":
				var err error
				outcome, err = svc.SubmitOpenLedgerTxDetailed(blob, false)
				require.NoError(t, err)
				require.True(t, outcome.Applied)
			case "local":
				var err error
				outcome, err = svc.SubmitOpenLedgerTxDetailed(blob, true)
				require.NoError(t, err)
				require.True(t, outcome.Applied)
			case "rpc":
				parsed, err := tx.ParseFromBinary(blob)
				require.NoError(t, err)
				result, err := svc.SubmitTransaction(parsed, blob, false)
				require.NoError(t, err)
				require.True(t, result.Applied)
				require.True(t, result.Result.IsSuccess(), "RPC submission returned %s", result.Result)
			case "txset":
				var err error
				outcome, err = svc.LearnOpenLedgerTx(blob)
				require.NoError(t, err)
				require.True(t, outcome.Applied)
			}

			exists, err := svc.OpenLedgerHasTx(hash)
			require.NoError(t, err)
			require.True(t, exists, "%s ingress did not publish the signed transaction", origin)
			require.Equal(t, uint64(256<<20), budget.persistence.Snapshot().Used)
			require.Zero(t, budget.speculative.Snapshot().Used)
			require.Zero(t, budget.pool.Snapshot().InvalidReleases)
		})
	}
}

func TestIssue2071IngressMemoryReleaseWakesExactlyOneRetry(t *testing.T) {
	budget := newIssue2071Budget(t, 8<<20, 8<<20, 8<<20, 0)
	svc := newIssue2071Service(t, budget)

	env := jtx.NewTestEnv(t)
	blob, hash := issue2071FundedPayment(t, svc, env, "issue-2071-ingress-wakeup")
	require.True(t, budget.speculative.TryReserve(8<<20))
	baselineSpeculative := budget.speculative.Snapshot()
	type result struct {
		outcome openledger.SubmitOutcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, false)
		done <- result{outcome: outcome, err: err}
	}()
	issue2071WaitForDenied(t, budget.speculative)
	beforeState, beforeTx := issue2071OpenHashes(t, svc)
	issue2071RequireGateFree(t, svc)

	budget.speculative.Release(8 << 20)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.True(t, got.outcome.Applied)
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not resume after speculative memory was released")
	}

	exists, err := svc.OpenLedgerHasTx(hash)
	require.NoError(t, err)
	require.True(t, exists)
	afterState, afterTx := issue2071OpenHashes(t, svc)
	require.NotEqual(t, beforeState, afterState, "the transaction must publish only after admission resumes")
	require.NotEqual(t, beforeTx, afterTx, "the transaction must publish only after admission resumes")
	snapshot := budget.speculative.Snapshot()
	require.Equal(t, baselineSpeculative.Denied+1, snapshot.Denied, "one temporary denial should produce one retry wakeup")
	require.Equal(t, baselineSpeculative.Subscriptions+2, snapshot.Subscriptions, "one denied attempt and its retry should register two wake channels")
	require.Zero(t, snapshot.Used)
	require.Zero(t, budget.pool.Snapshot().InvalidReleases)
}

func TestIssue2071SuccessorReleasesOwnershipBeforeWaiting(t *testing.T) {
	env := jtx.NewTestEnv(t)
	receiver := jtx.NewAccount("issue-2071-successor-wait")
	funding, _ := issue2071SignedPaymentAmount(t, env, jtx.MasterAccount(), receiver, 100_000_000, 1)
	blob, hash := issue2071SignedPayment(t, env, jtx.MasterAccount(), receiver, 2)
	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)
	retries := make([]openledger.PendingTx, 512)
	for i := range retries {
		retries[i] = pending
	}
	baseStats := openledger.TransactionSnapshotStats{}
	ingressNeed := preferredTransitionEstimate(baseStats, baseStats, baseStats, []openledger.PendingTx{pending})
	successorNeed := preferredTransitionEstimate(baseStats, baseStats, baseStats, retries)
	require.Greater(t, successorNeed, ingressNeed+issue2071MiB)

	speculativeMaximum := successorNeed + issue2071MiB
	budgetTemplate := newIssue2071Budget(t, 8<<20, 8<<20, speculativeMaximum, 0)
	svc := newIssue2071Service(t, budgetTemplate)
	fundingOutcome, err := svc.SubmitOpenLedgerTxDetailed(funding, false)
	require.NoError(t, err)
	require.True(t, fundingOutcome.Applied, "account funding returned %s", fundingOutcome.Result)
	parent := issue2071ClosedChild(t, svc.GetClosedLedger())
	remaining := successorNeed - 1
	hold := speculativeMaximum - remaining
	require.GreaterOrEqual(t, hold, uint64(1))
	require.True(t, budgetTemplate.speculative.TryReserve(hold))

	beforeState, beforeTx := issue2071OpenHashes(t, svc)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		prepared *PreparedPreferredLedger
		err      error
	}
	done := make(chan result, 1)
	go func() {
		prepared, err := svc.prepareSuccessor(ctx, parent, successorOptions{
			preferred: true,
			admitted:  true,
			retries:   retries,
		})
		done <- result{prepared: prepared, err: err}
	}()
	issue2071WaitForDenied(t, budgetTemplate.speculative)
	issue2071RequireGateFree(t, svc)
	afterState, afterTx := issue2071OpenHashes(t, svc)
	require.Equal(t, beforeState, afterState, "a denied successor must not mutate the published open view")
	require.Equal(t, beforeTx, afterTx, "a denied successor must not mutate the published open view")

	type ingressResult struct {
		outcome openledger.SubmitOutcome
		err     error
	}
	ingressDone := make(chan ingressResult, 1)
	go func() {
		outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, false)
		ingressDone <- ingressResult{outcome: outcome, err: err}
	}()
	select {
	case got := <-ingressDone:
		require.NoError(t, got.err)
		require.True(t, got.outcome.Applied, "older ingress was not applied")
	case <-time.After(5 * time.Second):
		t.Fatal("older ingress did not publish while successor admission was waiting")
	}
	afterState, afterTx = issue2071OpenHashes(t, svc)
	require.NotEqual(t, beforeState, afterState)
	require.NotEqual(t, beforeTx, afterTx)
	issue2071RequireGateFree(t, svc)

	cancel()
	select {
	case got := <-done:
		require.Nil(t, got.prepared)
		require.ErrorIs(t, got.err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled successor admission did not stop")
	}
	budgetTemplate.speculative.Release(hold)
	final := budgetTemplate.pool.Snapshot()
	require.Zero(t, final.Used)
	require.Zero(t, final.InvalidReleases)
	require.Equal(t, hash, pending.Hash)
}

func TestIssue2071SuccessorAdmissionCancellationReleasesOwnership(t *testing.T) {
	budget := newIssue2071Budget(t, 8<<20, 8<<20, 8<<20, 0)
	svc := newIssue2071Service(t, budget)
	require.True(t, budget.speculative.TryReserve(8<<20))
	parent := issue2071ClosedChild(t, svc.GetClosedLedger())
	ctx, cancel := context.WithCancel(t.Context())
	type result struct {
		prepared *PreparedPreferredLedger
		err      error
	}
	done := make(chan result, 1)
	go func() {
		prepared, err := svc.prepareSuccessor(ctx, parent, successorOptions{preferred: true, admitted: true})
		done <- result{prepared: prepared, err: err}
	}()
	issue2071WaitForDenied(t, budget.speculative)
	issue2071RequireGateFree(t, svc)
	cancel()
	select {
	case got := <-done:
		require.Nil(t, got.prepared)
		require.ErrorIs(t, got.err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("successor cancellation did not return")
	}
	require.False(t, svc.preferredTransitionIsActive())
	require.True(t, svc.openLedgerMu.TryLock())
	svc.openLedgerMu.Unlock()
	require.Equal(t, uint64(8<<20), budget.speculative.Snapshot().Used)
	budget.speculative.Release(8 << 20)
	require.Zero(t, budget.pool.Snapshot().Used)
	require.Zero(t, budget.pool.Snapshot().InvalidReleases)
}

func TestIssue2071SuccessorMemoryReleaseWakesAndPublishes(t *testing.T) {
	budget := newIssue2071Budget(t, 8<<20, 8<<20, 8<<20, 0)
	svc := newIssue2071Service(t, budget)
	parent := issue2071ClosedChild(t, svc.GetClosedLedger())
	require.True(t, budget.speculative.TryReserve(8<<20))
	baseline := budget.speculative.Snapshot()

	type result struct {
		prepared *PreparedPreferredLedger
		err      error
	}
	done := make(chan result, 1)
	go func() {
		prepared, err := svc.prepareSuccessor(t.Context(), parent, successorOptions{preferred: true, admitted: true})
		done <- result{prepared: prepared, err: err}
	}()
	issue2071WaitForDenied(t, budget.speculative)
	issue2071RequireGateFree(t, svc)
	budget.speculative.Release(8 << 20)

	var prepared *PreparedPreferredLedger
	select {
	case got := <-done:
		require.NoError(t, got.err)
		prepared = got.prepared
	case <-time.After(5 * time.Second):
		t.Fatal("successor did not resume after speculative memory was released")
	}
	require.NotNil(t, prepared)
	require.NoError(t, prepared.Wait(t.Context()))
	published, err := prepared.TryPublish()
	require.NoError(t, err)
	require.True(t, published)
	prepared.Finish()
	prepared.Release()

	require.Equal(t, parent, svc.GetClosedLedger())
	snapshot := budget.speculative.Snapshot()
	require.Equal(t, baseline.Denied+1, snapshot.Denied)
	require.Zero(t, snapshot.Used)
	require.Zero(t, budget.pool.Snapshot().InvalidReleases)
}

func TestIssue2071ShutdownAbortsWaitingIngress(t *testing.T) {
	budget := newIssue2071Budget(t, 8<<20, 8<<20, 8<<20, 0)
	svc := newIssue2071Service(t, budget)
	env := jtx.NewTestEnv(t)
	blob, _ := issue2071FundedPayment(t, svc, env, "issue-2071-shutdown")
	require.True(t, budget.speculative.TryReserve(8<<20))
	done := make(chan error, 1)
	go func() {
		_, err := svc.SubmitOpenLedgerTxDetailed(blob, false)
		done <- err
	}()
	issue2071WaitForDenied(t, budget.speculative)
	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, errServiceNotRunning)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not abort waiting ingress")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete after aborting ingress")
	}
	budget.speculative.Release(8 << 20)
	require.Zero(t, budget.pool.Snapshot().Used)
	require.Zero(t, budget.pool.Snapshot().InvalidReleases)
}

func TestIssue2071ShutdownAbortsWaitingSuccessor(t *testing.T) {
	budget := newIssue2071Budget(t, 8<<20, 8<<20, 8<<20, 0)
	svc := newIssue2071Service(t, budget)
	parent := issue2071ClosedChild(t, svc.GetClosedLedger())
	require.True(t, budget.speculative.TryReserve(8<<20))

	type result struct {
		prepared *PreparedPreferredLedger
		err      error
	}
	done := make(chan result, 1)
	go func() {
		prepared, err := svc.prepareSuccessor(t.Context(), parent, successorOptions{preferred: true, admitted: true})
		done <- result{prepared: prepared, err: err}
	}()
	issue2071WaitForDenied(t, budget.speculative)
	issue2071RequireGateFree(t, svc)

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case got := <-done:
		require.Nil(t, got.prepared)
		require.ErrorIs(t, got.err, errServiceNotRunning)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not abort waiting successor")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete after aborting successor")
	}
	require.False(t, svc.preferredTransitionIsActive())
	require.True(t, svc.openLedgerMu.TryLock())
	svc.openLedgerMu.Unlock()
	budget.speculative.Release(8 << 20)
	require.Zero(t, budget.pool.Snapshot().Used)
	require.Zero(t, budget.pool.Snapshot().InvalidReleases)
}

func TestIssue2071OversizedSuccessorAdmissionDoesNotWait(t *testing.T) {
	budget := newIssue2071Budget(t, 8<<20, 8<<20, 8<<20, 0)
	svc := newIssue2071Service(t, budget)
	parent := issue2071ClosedChild(t, svc.GetClosedLedger())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	baseline := budget.speculative.Snapshot()
	_, err := svc.prepareSuccessor(ctx, parent, successorOptions{
		preferred: true,
		admitted:  true,
		retries:   []openledger.PendingTx{{Blob: bytes.Repeat([]byte{0xa5}, 9<<20)}},
	})
	var memoryErr *speculativeMemoryError
	require.ErrorAs(t, err, &memoryErr)
	require.True(t, memoryErr.oversized)
	require.ErrorIs(t, err, ErrPreferredTransitionMemory)
	snapshot := budget.speculative.Snapshot()
	require.Equal(t, baseline.Denied, snapshot.Denied, "oversized requests must fail before TryReserve")
	require.Equal(t, baseline.Subscriptions, snapshot.Subscriptions, "oversized requests must fail before Changed")
	require.Zero(t, snapshot.Used)
	require.Zero(t, budget.pool.Snapshot().InvalidReleases)
	require.False(t, svc.preferredTransitionIsActive())
	require.True(t, svc.openLedgerMu.TryLock())
	svc.openLedgerMu.Unlock()
}
