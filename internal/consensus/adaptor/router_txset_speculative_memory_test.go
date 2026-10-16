package adaptor

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func issue2071RouterPayment(t *testing.T, env *jtx.TestEnv, receiver *jtx.Account, amount uint64, sequence uint32) ([]byte, [32]byte) {
	t.Helper()
	env.SetVerifySignatures(true)
	transaction := payment.Pay(jtx.MasterAccount(), receiver, amount).Sequence(sequence).Build()
	env.SignWith(transaction, jtx.MasterAccount())
	txMap, err := transaction.Flatten()
	require.NoError(t, err)
	hexString, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexString)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	return blob, hash
}

func TestRouterTxSetLearningUsesSpeculativeMemoryWhilePersistenceIsHeld(t *testing.T) {
	pool := membudget.New(512 << 20)
	persistence, err := pool.NewAccount("persistence", 256<<20, 256<<20)
	require.NoError(t, err)
	speculative, err := pool.NewAccount("speculative", 0, 256<<20)
	require.NoError(t, err)
	svc, err := ledgerservice.New(ledgerservice.Config{
		Standalone:        true,
		GenesisConfig:     genesis.DefaultConfig(),
		PersistenceMemory: persistence,
		SpeculativeMemory: speculative,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	receiver := jtx.NewAccount("issue-2071-router-txset")
	funding, _ := issue2071RouterPayment(t, env, receiver, 100_000_000, 1)
	fundingOutcome, err := svc.SubmitOpenLedgerTxDetailed(funding, false)
	require.NoError(t, err)
	require.True(t, fundingOutcome.Applied, "account funding returned %s", fundingOutcome.Result)
	target, targetHash := issue2071RouterPayment(t, env, receiver, 1_000_000, 2)

	txMap := shamap.New(shamap.TypeTransaction)
	require.NoError(t, txMap.PutWithNodeType(targetHash, target, shamap.NodeTypeTransactionNoMeta))
	setHash, err := txMap.Hash()
	require.NoError(t, err)
	wireNodes, err := txMap.WalkWireNodes()
	require.NoError(t, err)
	require.NotEmpty(t, wireNodes)
	data := &message.LedgerData{
		LedgerHash: setHash[:],
		InfoType:   message.LedgerInfoTsCandidate,
	}
	for _, node := range wireNodes {
		data.Nodes = append(data.Nodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}

	require.True(t, persistence.TryReserve(256<<20))
	defer persistence.Release(256 << 20)

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	adaptor := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	engine := &mockEngine{}
	router := newTestRouter(engine, adaptor, nil)
	router.MarkTxSetStillNeeded(consensus.TxSetID(setHash))

	// The complete set reaches consensus synchronously while the acquired leaf
	// takes the tx-set learning route. That route must use the sibling account.
	router.handleTxSetData(data, 7)
	engine.mu.Lock()
	require.Equal(t, []consensus.TxSetID{consensus.TxSetID(setHash)}, engine.txSets)
	engine.mu.Unlock()

	exists, err := svc.OpenLedgerHasTx(targetHash)
	require.NoError(t, err)
	require.True(t, exists, "tx-set learning did not publish the acquired transaction")
	require.Equal(t, uint64(256<<20), persistence.Snapshot().Used)
	require.Zero(t, speculative.Snapshot().Used)
	require.Zero(t, pool.Snapshot().InvalidReleases)
}
