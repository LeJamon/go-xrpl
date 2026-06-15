package adaptor

import (
	"io"
	"log/slog"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func serveTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestServeLedger_BaseAndStateRoundTripToAcquisition exercises the full
// go-xrpl→go-xrpl ledger-acquisition wire path: the serve side builds the liBASE
// reply (header + state root) and answers liAS_NODE requests, and a fresh
// inbound.Ledger acquires the ledger from those replies alone. This is the
// counterpart to rippled's PeerImp::sendLedgerBase + processLedgerRequest
// (PeerImp.cpp:3119-3411) — before the serve side returned more than the bare
// header, a go-xrpl node could not catch up from another go-xrpl node.
func TestServeLedger_BaseAndStateRoundTripToAcquisition(t *testing.T) {
	t.Parallel()
	adaptor, _ := newTxSetWireAdaptor(t)
	router := NewRouter(&mockEngine{}, adaptor, nil)

	l := adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, l)
	hash := l.Hash()
	seq := l.Sequence()

	// Serve the base: header (118-byte wire form, no trailing hash) + state root.
	// The genesis ledger has no transactions, so there is no node[2].
	baseNodes := router.buildLedgerBaseNodes(l)
	require.GreaterOrEqual(t, len(baseNodes), 2, "base must carry header + state root")
	require.Equal(t, header.SizeBase, len(baseNodes[0].NodeData),
		"node[0] must be the 118-byte rippled wire header (no trailing hash)")

	// Acquire: feed the served base into a fresh acquisition.
	il := inbound.New(hash, seq, 7, serveTestLogger())
	require.NoError(t, il.GotBase(baseNodes))

	// Drive the state fetch: request the outstanding nodes, serve them, repeat.
	for i := 0; i < 200 && !il.IsComplete(); i++ {
		ids := il.NeedsMissingNodeIDs()
		require.NotEmpty(t, ids, "incomplete acquisition must list outstanding state nodes")
		req := &message.GetLedger{
			InfoType:   message.LedgerInfoAsNode,
			LedgerHash: hash[:],
			LedgerSeq:  seq,
			NodeIDs:    ids,
		}
		nodes := router.serveLedgerMapNodes(l.StateMapSnapshot, req, 7, "state")
		require.NotEmpty(t, nodes, "serve must answer the requested state node IDs")
		require.NoError(t, il.GotStateNodes(nodes))
	}

	require.True(t, il.IsComplete(), "go-xrpl-served ledger must fully acquire")

	gotHdr, gotState, gotTx, err := il.Result()
	require.NoError(t, err)
	require.Equal(t, hash, gotHdr.Hash)
	require.Nil(t, gotTx, "genesis tx tree is empty → nil tx map")

	gotStateHash, err := gotState.Hash()
	require.NoError(t, err)
	wantStateHash, err := l.StateMapHash()
	require.NoError(t, err)
	require.Equal(t, wantStateHash, gotStateHash,
		"acquired state root must match the served ledger")
}

// TestBuildLedgerBaseNodes_EmptyTxTreeOmitsTxRoot pins that a ledger with no
// transactions yields exactly header + state root (no node[2]), mirroring
// rippled sendLedgerBase which appends the tx root only when the tx tree is
// non-empty (PeerImp.cpp:3139-3148).
func TestBuildLedgerBaseNodes_EmptyTxTreeOmitsTxRoot(t *testing.T) {
	t.Parallel()
	adaptor, _ := newTxSetWireAdaptor(t)
	router := NewRouter(&mockEngine{}, adaptor, nil)

	l := adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, l)

	txHash, err := l.TxMapHash()
	require.NoError(t, err)
	require.Equal(t, [32]byte{}, txHash, "fixture precondition: genesis has an empty tx tree")

	nodes := router.buildLedgerBaseNodes(l)
	require.Len(t, nodes, 2, "empty tx tree must omit the tx root (header + state root only)")
}
