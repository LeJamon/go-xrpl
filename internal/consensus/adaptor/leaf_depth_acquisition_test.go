package adaptor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

// newStoredLeafDepthAcquisition tracks a backed acquisition whose state root
// commits to a single-branch chain of inner nodes ending in an inner node at
// leaf depth. Every node below the root is already in the store, so only a
// missing-node walk can discover the impossible shape.
func newStoredLeafDepthAcquisition(t *testing.T, r *Router) *inbound.Ledger {
	t.Helper()
	const leafDepth = 64
	family := backend.NewMemory()
	entries := make([]shamap.FlushEntry, 0, leafDepth)
	child := [32]byte{1}
	var rootWire []byte
	for depth := leafDepth; depth >= 0; depth-- {
		hashes := make([]byte, shamap.BranchFactor*32)
		copy(hashes, child[:])
		hash := sha512half.Sum(protocol.HashPrefixInnerNode().Bytes(), hashes)
		if depth == 0 {
			rootWire = append(append([]byte{}, child[:]...), 0, byte(protocol.WireTypeCompressedInner))
		} else {
			data := append(append([]byte{}, protocol.HashPrefixInnerNode().Bytes()...), hashes...)
			entries = append(entries, shamap.FlushEntry{Hash: hash, Data: data, LedgerSeq: 2, MapType: shamap.TypeState})
		}
		child = hash
	}
	require.NoError(t, family.StoreBatch(t.Context(), entries))

	seq := r.adaptor.LedgerService().GetClosedLedger().Sequence() + 1
	headerData := header.AddRaw(header.LedgerHeader{LedgerIndex: seq, AccountHash: child}, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	ledger := inbound.New(ledgerHash, seq, 7, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, ledger.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootWire}}))
	r.catchupReplay.fetchTracker.Track(ledger)
	return ledger
}

// A walk that finds an inner node at leaf depth fails the acquisition, as a
// consensus failure that blocks an immediate retry, without charging any peer.
func TestWalkDiscoveredInvalidMapFailsAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(r *Router, ledger *inbound.Ledger)
	}{
		{"request path", func(r *Router, ledger *inbound.Ledger) {
			r.catchupReplay.requestMissingAcquisitionNodes(ledger, 0)
		}},
		{"inline work path", func(r *Router, ledger *inbound.Ledger) {
			require.Nil(t, r.catchupReplay.currentAcquisitionWork(), "the inline path runs without a work lane")
			require.True(t, r.catchupReplay.submitAcquisitionWork(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimer, peers: []uint64{7}}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender := makeRouterWithBadDataRecorder(t)
			ledger := newStoredLeafDepthAcquisition(t, r)
			hash := ledger.Hash()

			tc.run(r, ledger)

			require.Nil(t, r.catchupReplay.fetchTracker.Find(hash), "acquisition removed")
			require.True(t, r.catchupReplay.catchupRetryBlocked(hash, time.Now()), "failed, not discarded")
			require.Empty(t, sender.getBadDataCalls(), "no peer is charged for a walk verdict")
		})
	}
}
