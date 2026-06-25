package adaptor

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// signedPaymentFrame builds a real signed master→alice Payment and
// returns its wire blob plus the expected transaction hash. The
// open-ledger Submit path rejects un-parseable blobs, so a tx must be a
// genuine signed Payment for HasTx to report true after dispatch.
func signedPaymentFrame(t *testing.T, env *testenv.TestEnv, seq uint32) ([]byte, consensus.TxID) {
	t.Helper()
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")
	txn := payment.Pay(master, alice, 100_000_000).Sequence(seq).Build()
	env.SignWith(txn, master)
	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	txHash, err := tx.ComputeTransactionHash(txn)
	require.NoError(t, err)
	return blob, consensus.TxID(txHash)
}

// TestSubmitTxJobShedsWhenPoolSaturated verifies that submitTxJob drops the
// frame and bumps the shed counter when the worker-pool queue is full, rather
// than blocking the consensus Run loop. Deterministic without goroutine timing:
// a depth-1 queue is pre-filled and no worker drains it, so the non-blocking
// send sheds on every call.
func TestSubmitTxJobShedsWhenPoolSaturated(t *testing.T) {
	r := NewRouter(&mockEngine{}, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))

	// Install a full queue with no drainers so every submit sheds.
	r.txJobs = make(chan *peermanagement.InboundMessage, 1)
	r.txJobs <- &peermanagement.InboundMessage{}

	require.Equal(t, uint64(0), r.DroppedTxJobs())

	const sheds = 5
	for range sheds {
		r.submitTxJob(&peermanagement.InboundMessage{
			PeerID: 7,
			Tx:     &message.Transaction{RawTransaction: []byte{0x01}},
		})
	}

	require.Equal(t, uint64(sheds), r.DroppedTxJobs())
}

// TestSubmitTxJobInlineFallback verifies that when the worker pool has not been
// started (r.txJobs == nil, the contract for tests that drive dispatch
// synchronously), submitTxJob runs handleTransaction inline on the calling
// goroutine. The transaction is observable via HasTx immediately, with no
// sleep — that absence of a sleep is the assertion that the path is synchronous.
func TestSubmitTxJobInlineFallback(t *testing.T) {
	a := newTestAdaptor(t)
	r := NewRouter(&mockEngine{}, a, make(chan *peermanagement.InboundMessage, 1))
	require.Nil(t, r.txJobs, "pool must be unstarted so submitTxJob takes the inline path")

	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)
	blob, txID := signedPaymentFrame(t, env, 1)

	r.submitTxJob(&peermanagement.InboundMessage{
		PeerID: 3,
		Tx:     &message.Transaction{RawTransaction: blob, Status: message.TxStatusNew},
	})

	require.True(t, a.HasTx(txID),
		"inline path must apply the tx synchronously before submitTxJob returns")
}

// TestRunDrainsTxLane verifies the end-to-end #1103 path on the consensus
// side: a transaction pushed onto the dedicated tx lane (SetTxInbox) is
// drained by Run, handed to the worker pool, and applied — independent of
// the consensus inbox, which here is nil and never selected.
func TestRunDrainsTxLane(t *testing.T) {
	a := newTestAdaptor(t)
	// nil consensus inbox: that select case is never ready, so Run is
	// driven solely by the tx lane and the maintenance ticker.
	r := NewRouter(&mockEngine{}, a, nil)

	txLane := make(chan *peermanagement.InboundMessage, 4)
	r.SetTxInbox(txLane)

	go r.Run(t.Context())

	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)
	blob, txID := signedPaymentFrame(t, env, 1)

	txLane <- &peermanagement.InboundMessage{
		PeerID: 3,
		Tx:     &message.Transaction{RawTransaction: blob, Status: message.TxStatusNew},
	}

	require.Eventually(t, func() bool { return a.HasTx(txID) },
		2*time.Second, 5*time.Millisecond,
		"Run must drain the tx lane and apply the transaction")
}

// TestSubmitTxJobConcurrent exercises the real worker pool under concurrent
// submits for the race detector: txWorkerCount workers drain a shared channel
// while many goroutines submit at once. With fewer submits than txQueueDepth the
// buffer absorbs them all, so nothing is shed and the result is deterministic;
// `go test -race` is what makes this test meaningful — it verifies the
// channel / atomic-counter / worker handoff is free of data races.
func TestSubmitTxJobConcurrent(t *testing.T) {
	r := NewRouter(&mockEngine{}, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))

	// Workers exit when t.Context() is canceled at test cleanup, so they
	// don't leak across tests.
	r.startTxWorkers(t.Context())

	const n = 500 // < txQueueDepth (1024): the buffer absorbs all, so 0 sheds
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			r.submitTxJob(&peermanagement.InboundMessage{
				PeerID: 9,
				Tx:     &message.Transaction{RawTransaction: []byte{0x01}},
			})
		}()
	}
	wg.Wait()

	require.Equal(t, uint64(0), r.DroppedTxJobs())
}
