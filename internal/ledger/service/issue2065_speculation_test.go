package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestIssue2065AcceptancePublishesSkeletonBeforeSpeculation(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	master := jtx.MasterAccount()
	env := jtx.NewTestEnv(t)
	cold, family := successorPreparationParent(t, svc.GetClosedLedger(), master.AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	destination := jtx.NewAccount("issue-2065-successor")
	local, localHash := preferredSwitchPaymentBlob(t, env, master, destination, 20_000_000, 100, 1)
	localTx, err := openledger.ParsePendingTx(local)
	require.NoError(t, err)
	svc.localTxs.PushBack(svc.openLedgerView.Current().Sequence(), localTx)
	family.armed.Store(true)

	accepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), cold, nil, nil, cold.CloseTime().Add(10*time.Second), true)
		accepted <- err
	}()

	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptance did not enter speculative successor work")
	}

	select {
	case err := <-accepted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("consensus acceptance waited for speculative successor work")
	}

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	require.Equal(t, cold.Sequence()+1, closed.Sequence())
	require.Equal(t, [32]byte{}, closed.Header().TxHash)
	require.Equal(t, closed.Hash(), svc.GetOpenLedger().ParentHash())
	exists, err := svc.OpenLedgerHasTx(localHash)
	require.NoError(t, err)
	require.False(t, exists, "local input must remain outside the published empty skeleton")

	family.unblock()
	require.Eventually(t, func() bool {
		exists, err := svc.OpenLedgerHasTx(localHash)
		return err == nil && exists
	}, 5*time.Second, time.Millisecond, "successor worker did not promote local input")
	require.Same(t, closed, svc.GetClosedLedger())
}

func TestIssue2065SupersededSpeculationRetainsInputsWithoutStalePublication(t *testing.T) {
	secondSender := jtx.NewAccount("issue-2065-supersede-sender")
	cfg := DefaultConfig()
	cfg.GenesisConfig.InitialAccounts = []genesis.InitialAccount{{
		Address: secondSender.Address,
		Balance: 100_000_000_000,
	}}
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	master := jtx.MasterAccount()
	env := jtx.NewTestEnv(t)
	cold, family := successorPreparationParent(t, svc.GetClosedLedger(), master.AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	destination := jtx.NewAccount("issue-2065-supersede")
	first, firstHash := preferredSwitchPaymentBlob(t, env, master, destination, 20_000_000, 100, 1)
	firstTx, err := openledger.ParsePendingTx(first)
	require.NoError(t, err)
	svc.localTxs.PushBack(svc.openLedgerView.Current().Sequence(), firstTx)
	family.armed.Store(true)

	firstAccepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), cold, nil, nil, cold.CloseTime().Add(10*time.Second), true)
		firstAccepted <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first acceptance did not enter speculative successor work")
	}
	select {
	case err := <-firstAccepted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("first acceptance did not publish before speculation")
	}

	firstClosed := svc.GetClosedLedger()
	require.Equal(t, cold.Sequence()+1, firstClosed.Sequence())
	second, secondHash := preferredSwitchPaymentBlob(t, env, secondSender, destination, 1_000_000, 100, 1)

	secondAccepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), firstClosed, nil, [][]byte{second}, firstClosed.CloseTime().Add(10*time.Second), true)
		secondAccepted <- err
	}()
	select {
	case err := <-secondAccepted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("a later acceptance waited for the superseded speculative worker")
	}

	secondClosed := svc.GetClosedLedger()
	require.Equal(t, firstClosed.Sequence()+1, secondClosed.Sequence())
	require.Equal(t, [32]byte{}, secondClosed.Header().TxHash)
	require.Equal(t, secondClosed.Hash(), svc.GetOpenLedger().ParentHash())

	family.unblock()
	for _, hash := range [][32]byte{firstHash, secondHash} {
		require.Eventually(t, func() bool {
			exists, err := svc.OpenLedgerHasTx(hash)
			return err == nil && exists
		}, 5*time.Second, time.Millisecond, "successor promotion dropped local input %x", hash)
	}
	require.Same(t, secondClosed, svc.GetClosedLedger())
	require.Equal(t, secondClosed.Hash(), svc.GetOpenLedger().ParentHash(),
		"a stale worker must not republish the first round's open parent")
}
