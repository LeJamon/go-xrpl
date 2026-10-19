package adaptor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func testLedgerServeWorkspace() *ledgerServeWorkspace {
	return &ledgerServeWorkspace{wire: ledgerServeReplyBytes, nodes: txSetHardMaxReplyNodes}
}

type workspaceServeNetwork struct {
	noopSender
	send  func([]byte) error
	depth func() bool
}

func (n *workspaceServeNetwork) SendToPeer(_ uint64, frame []byte) error {
	return n.send(frame)
}

func (n *workspaceServeNetwork) PeerSupportsNodeDepth(uint64) bool {
	return n.depth != nil && n.depth()
}

func workspaceTxSetRequest(t *testing.T, a *Adaptor) *message.GetLedger {
	t.Helper()
	ts, err := a.BuildTxSet([][]byte{bytes.Repeat([]byte{0x42}, 128), bytes.Repeat([]byte{0x57}, 256)})
	require.NoError(t, err)
	hash := ts.ID()
	return &message.GetLedger{LedgerHash: hash[:], InfoType: message.LedgerInfoTsCandidate, NodeIDs: [][]byte{rootNodeID()}}
}

func TestLedgerServeWorkspaceRelease(t *testing.T) {
	for _, kind := range []message.LedgerInfoType{message.LedgerInfoBase, message.LedgerInfoAsNode, message.LedgerInfoTsCandidate} {
		for _, outcome := range []string{"accepted", "rejected", "disconnected", "canceled_before_encode"} {
			t.Run(fmt.Sprint(kind)+"/"+outcome, func(t *testing.T) {
				a, _ := newTxSetWireAdaptor(t)
				r := newTestRouter(&mockEngine{}, a, nil)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				req := workspaceTxSetRequest(t, a)
				if kind != message.LedgerInfoTsCandidate {
					l := a.LedgerService().GetClosedLedger()
					hash := l.Hash()
					req.LedgerHash, req.LedgerSeq, req.InfoType = hash[:], l.Sequence(), kind
				}
				var sends int
				r.serve = &workspaceServeNetwork{
					depth: func() bool {
						if outcome == "canceled_before_encode" {
							cancel()
						}
						return false
					},
					send: func(frame []byte) error {
						sends++
						require.Positive(t, r.serveMemory.Snapshot().Used, "caller frame stays reserved through admission")
						_, decoded := decodeFrame(t, frame)
						require.Equal(t, kind, decoded.(*message.LedgerData).InfoType)
						switch outcome {
						case "rejected":
							return peermanagement.ErrSendBufferFull
						case "disconnected":
							return errors.New("peer disconnected")
						default:
							return nil
						}
					},
				}
				r.handleGetLedger(ctx, &peermanagement.InboundMessage{PeerID: 7, GetLedger: req})
				if outcome == "canceled_before_encode" {
					require.Zero(t, sends)
				} else {
					require.Equal(t, 1, sends)
				}
				stats := r.serveMemory.Snapshot()
				require.Positive(t, stats.Peak)
				require.Zero(t, stats.Used)
				require.Zero(t, stats.InvalidReleases)
			})
		}
	}
}

func TestLedgerServeWorkspaceConcurrentCeiling(t *testing.T) {
	a, _ := newTxSetWireAdaptor(t)
	r := newTestRouter(&mockEngine{}, a, nil)
	r.serveReplyBytes = 2048
	req := workspaceTxSetRequest(t, a)
	reservation := ledgerServeWorkspaceBytes(r.serveReplyBytes, txSetHardMaxReplyNodes)
	account, err := membudget.New(2*reservation).NewAccount("peer_serve", 0, 2*reservation)
	require.NoError(t, err)
	require.NoError(t, r.SetLedgerServeMemoryBudget(account))
	entered, release := make(chan struct{}, 8), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var sends atomic.Int32
	r.serve = &workspaceServeNetwork{send: func(frame []byte) error {
		if len(frame) > r.serveReplyBytes {
			t.Errorf("oversized reply: %d", len(frame))
		}
		sends.Add(1)
		entered <- struct{}{}
		<-release
		return nil
	}}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.serveTxSet(t.Context(), 7, req)
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			unblock()
			t.Fatal("admitted serving workers did not reach queue admission")
		}
	}
	require.Eventually(t, func() bool { return account.Snapshot().Denied == 6 }, time.Second, time.Millisecond)
	require.Equal(t, 2*reservation, account.Snapshot().Used)
	require.Equal(t, 2*reservation, account.Snapshot().Peak)
	unblock()
	workers.Wait()
	require.Equal(t, int32(2), sends.Load())
	require.Zero(t, account.Snapshot().Used)
	require.Zero(t, account.Snapshot().InvalidReleases)
}

func TestLedgerServeWorkspaceCannotConsumeProgressReserve(t *testing.T) {
	r := newTestRouter(&mockEngine{}, nil, nil)
	bytes := ledgerServeWorkspaceBytes(r.serveReplyBytes, txSetHardMaxReplyNodes)
	pool := membudget.New(bytes)
	progress, err := pool.NewAccount("persistence", bytes, bytes)
	require.NoError(t, err)
	serving, err := pool.NewAccount("peer_serve", 0, bytes)
	require.NoError(t, err)
	require.NoError(t, r.SetLedgerServeMemoryBudget(serving))
	require.Nil(t, r.reserveLedgerServeWorkspace(t.Context(), false))
	require.True(t, progress.TryReserve(bytes))
	progress.Release(bytes)
	require.Zero(t, pool.Snapshot().Used)
	require.Zero(t, pool.Snapshot().InvalidReleases)
}

func TestLedgerServeWorkspaceEarlyReturns(t *testing.T) {
	for _, outcome := range []string{"empty", "encode_failure", "missing_ledger", "missing_path"} {
		t.Run(outcome, func(t *testing.T) {
			a, _ := newTxSetWireAdaptor(t)
			r := newTestRouter(&mockEngine{}, a, nil)
			req := workspaceTxSetRequest(t, a)
			switch outcome {
			case "empty":
				req.NodeIDs = nil
			case "encode_failure":
				r.serveReplyBytes = 1
			case "missing_ledger":
				req.InfoType = message.LedgerInfoAsNode
				req.LedgerHash = bytes.Repeat([]byte{0xff}, 32)
			case "missing_path":
				l := a.LedgerService().GetClosedLedger()
				hash := l.Hash()
				req.LedgerHash, req.InfoType = hash[:], message.LedgerInfoAsNode
				req.NodeIDs = [][]byte{append(bytes.Repeat([]byte{0xff}, 32), 64)}
			}
			var sends int
			r.serve = &workspaceServeNetwork{send: func([]byte) error { sends++; return nil }}
			r.handleGetLedger(t.Context(), &peermanagement.InboundMessage{PeerID: 7, GetLedger: req})
			if outcome == "empty" {
				require.Equal(t, 1, sends)
			} else {
				require.Zero(t, sends)
			}
			stats := r.serveMemory.Snapshot()
			require.Positive(t, stats.Peak)
			require.Zero(t, stats.Used)
			require.Zero(t, stats.InvalidReleases)
		})
	}
}

func TestLedgerServeOpenLedgerDoesNotFlushDirtyMaps(t *testing.T) {
	for _, kind := range []message.LedgerInfoType{message.LedgerInfoBase, message.LedgerInfoAsNode, message.LedgerInfoTxNode} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			fixture := newRouterServeFixture(t, false)
			svc := fixture.router.adaptor.LedgerService()
			open := svc.GetOpenLedger()
			require.NotNil(t, open)
			require.True(t, open.IsOpen())
			open.SetSHAMapFamily(fixture.family)
			require.NoError(t, open.Insert(keylet.Keylet{Key: [32]byte{0x42}}, []byte("dirty state")))
			require.NoError(t, open.AddTransaction([32]byte{0x57}, []byte("dirty transaction")))
			found, err := svc.GetLedgerBySequence(open.Sequence())
			require.NoError(t, err)
			require.Same(t, open, found, "sequence lookup exposes the current open view")
			stores := fixture.family.stores.Load()

			fixture.sender.ledgerPeer, fixture.sender.ledgerOK = 99, true
			query := message.QueryTypeIndirect
			req := &message.GetLedger{
				InfoType: kind, LedgerSeq: open.Sequence(),
				NodeIDs: [][]byte{rootNodeID()}, QueryType: &query,
			}
			for range 2 {
				fixture.router.handleMessage(&peermanagement.InboundMessage{
					PeerID: 7, Type: message.TypeGetLedger, Payload: encodePayload(t, req),
				})
				require.Empty(t, fixture.sender.sentFrames(), "open-ledger sequence requests must neither serve nor relay")
				require.Empty(t, fixture.sender.badDataCalls())
				require.Equal(t, stores, fixture.family.stores.Load(), "serving must not persist the dirty open view")
				require.Zero(t, fixture.router.serveMemory.Snapshot().Used)
				require.Zero(t, fixture.router.serveMemory.Snapshot().InvalidReleases)
			}

			_, err = open.StateMapSnapshotContext(t.Context())
			require.NoError(t, err)
			require.Greater(t, fixture.family.stores.Load(), stores, "fixture must retain dirty backed state")
			stores = fixture.family.stores.Load()
			_, err = open.TxMapSnapshotContext(t.Context())
			require.NoError(t, err)
			require.Greater(t, fixture.family.stores.Load(), stores, "fixture must retain dirty backed transactions")

			fixture.router.handleMessage(&peermanagement.InboundMessage{
				PeerID: 7, Type: message.TypeGetLedger,
				Payload: encodePayload(t, &message.GetLedger{
					InfoType: message.LedgerInfoBase, LedgerSeq: svc.GetClosedLedger().Sequence(),
				}),
			})
			sent := fixture.sender.sentFrames()
			require.Len(t, sent, 1, "closed-ledger serving remains available")
			msgType, decoded := decodeFrame(t, sent[0].frame)
			require.Equal(t, message.TypeLedgerData, msgType)
			require.Equal(t, svc.GetClosedLedger().Sequence(), decoded.(*message.LedgerData).LedgerSeq)
		})
	}
}
