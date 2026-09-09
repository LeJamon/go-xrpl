package consensus

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	consensus "github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	ledgerpkg "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestConsensusAcceptanceConvergesAcrossFullValidators(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	t.Cleanup(cluster.Stop)
	for _, node := range cluster.Nodes {
		t.Cleanup(node.Service.Stop)
	}

	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Amendments = nil
	initial, err := genesis.Create(genesisConfig)
	require.NoError(t, err)
	genesisLedger, err := ledgerpkg.FromGenesis(initial.Header, initial.StateMap, initial.TxMap, drops.Fees{})
	require.NoError(t, err)
	fixedTime := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	parent, err := ledgerpkg.NewOpen(genesisLedger, fixedTime)
	require.NoError(t, err)
	require.NoError(t, parent.Close(fixedTime, 0))
	for _, node := range cluster.Nodes {
		snapshot, err := parent.Snapshot()
		require.NoError(t, err)
		require.NoError(t, node.Service.SwitchToPreferredLedger(snapshot))
	}

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	agreedBlob := signedPaymentBlob(
		t,
		env,
		master,
		jtx.NewAccount("acceptance-agreed-destination"),
		1_000_000_000,
		1,
	)
	agreed, err := openledger.ParsePendingTx(agreedBlob)
	require.NoError(t, err)

	speculative := make([]openledger.PendingTx, len(cluster.Nodes))
	for i, node := range cluster.Nodes {
		blob := signedPaymentBlob(
			t,
			env,
			master,
			jtx.NewAccount(fmt.Sprintf("acceptance-speculative-%d", i)),
			100_000_000,
			1,
		)
		ptx, err := openledger.ParsePendingTx(blob)
		require.NoError(t, err)
		speculative[i] = ptx

		result, err := node.Service.SubmitOpenLedgerTx(blob, false)
		require.NoError(t, err)
		require.Equal(t, openledger.ResultSuccess, result, "speculative ingress at node %d", i)
		hasTx, err := node.Service.OpenLedgerHasTx(ptx.Hash)
		require.NoError(t, err)
		require.True(t, hasTx, "speculative transaction missing from node %d open view", i)
	}

	closeTime := parent.CloseTime().Add(10 * time.Second)
	closed := make([][32]byte, len(cluster.Nodes))
	closedSeq := parent.Sequence() + 1
	for i, node := range cluster.Nodes {
		seq, err := node.Service.AcceptConsensusResult(
			t.Context(),
			parent,
			[][]byte{agreedBlob},
			nil,
			closeTime,
			true,
		)
		require.NoError(t, err, "acceptance at node %d", i)
		require.Equal(t, closedSeq, seq)

		ledger := node.Service.GetClosedLedger()
		require.NotNil(t, ledger)
		require.Equal(t, closedSeq, ledger.Sequence())
		closed[i] = ledger.Hash()

		hasAgreed, err := ledger.TxExists(agreed.Hash)
		require.NoError(t, err)
		require.True(t, hasAgreed, "agreed transaction missing at node %d", i)
		hasSpeculative, err := ledger.TxExists(speculative[i].Hash)
		require.NoError(t, err)
		require.False(t, hasSpeculative, "speculative transaction entered closed ledger at node %d", i)
	}
	for i := 1; i < len(closed); i++ {
		require.Equal(t, closed[0], closed[i], "closed ledger hash at node %d", i)
	}

	validations := make([]*consensus.Validation, len(cluster.Nodes))
	for i, node := range cluster.Nodes {
		validations[i] = &consensus.Validation{
			LedgerID:  consensus.LedgerID(closed[0]),
			LedgerSeq: closedSeq,
			NodeID:    node.Identity.NodeID,
			SignTime:  closeTime,
			SeenTime:  closeTime,
			Full:      true,
		}
		require.NoError(t, node.Adaptor.SignValidation(validations[i]))
	}
	for i, node := range cluster.Nodes {
		node.Adaptor.OnConsensusReached(adaptor.WrapLedger(node.Service.GetClosedLedger()), validations, time.Second)
		node.Adaptor.OnLedgerFullyValidated(consensus.LedgerID(closed[0]), closedSeq)

		require.Equal(t, consensus.OpModeFull, node.Adaptor.GetOperatingMode(), "operating mode at node %d", i)
		require.Equal(t, closed[0], [32]byte(node.Adaptor.GetValidatedLedgerHash()), "validated hash at node %d", i)
		require.Equal(t, closedSeq, node.Service.GetValidatedLedgerIndex(), "validated sequence at node %d", i)
	}
}

func TestConsensusAcceptanceCarriesIngressQueuedDuringPublication(t *testing.T) {
	cluster := NewTestCluster(t, 1)
	t.Cleanup(cluster.Stop)
	node := cluster.Nodes[0]
	t.Cleanup(node.Service.Stop)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	firstBlob := signedPaymentBlob(
		t,
		env,
		master,
		jtx.NewAccount("acceptance-publication-first"),
		1_000_000_000,
		1,
	)
	first, err := openledger.ParsePendingTx(firstBlob)
	require.NoError(t, err)
	result, err := node.Service.SubmitOpenLedgerTx(firstBlob, true)
	require.NoError(t, err)
	require.Equal(t, openledger.ResultSuccess, result)

	relayEntered := make(chan struct{})
	releaseRelay := make(chan struct{})
	var relayOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRelay) }) }
	node.Service.SetTxRelay(func(blob []byte) {
		ptx, err := openledger.ParsePendingTx(blob)
		if err != nil || ptx.Hash != first.Hash {
			return
		}
		relayOnce.Do(func() { close(relayEntered) })
		<-releaseRelay
	})
	t.Cleanup(func() {
		release()
		node.Service.SetTxRelay(nil)
	})

	parent := node.Service.GetClosedLedger()
	accepted := make(chan error, 1)
	go func() {
		_, err := node.Service.AcceptConsensusResult(
			t.Context(),
			parent,
			nil,
			nil,
			parent.CloseTime().Add(10*time.Second),
			true,
		)
		accepted <- err
	}()
	select {
	case <-relayEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptance did not reach next-open replay")
	}

	secondBlob := signedPaymentBlob(
		t,
		env,
		master,
		jtx.NewAccount("acceptance-publication-second"),
		100_000_000,
		2,
	)
	second, err := openledger.ParsePendingTx(secondBlob)
	require.NoError(t, err)
	submitted := make(chan struct {
		result openledger.Result
		err    error
	}, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result, err := node.Service.SubmitOpenLedgerTx(secondBlob, true)
		submitted <- struct {
			result openledger.Result
			err    error
		}{result: result, err: err}
	}()
	<-started
	select {
	case <-submitted:
		t.Fatal("ingress completed before detached publication was released")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	require.NoError(t, <-accepted)
	submission := <-submitted
	require.NoError(t, submission.err)
	require.Equal(t, openledger.ResultSuccess, submission.result)

	hasFirst, err := node.Service.OpenLedgerHasTx(first.Hash)
	require.NoError(t, err)
	require.True(t, hasFirst, "pre-existing local transaction was lost during publication")
	hasSecond, err := node.Service.OpenLedgerHasTx(second.Hash)
	require.NoError(t, err)
	require.True(t, hasSecond, "ingress queued during publication was lost")
	require.Zero(t, node.Service.GetClosedLedger().TxCount(), "speculative ingress leaked into empty agreed set")
}
