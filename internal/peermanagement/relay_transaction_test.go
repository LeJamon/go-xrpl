package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

func relayTestPeer(t *testing.T, ident *Identity, id PeerID, txrr bool) *Peer {
	t.Helper()
	p := NewPeer(id, Endpoint{Host: "127.0.0.1", Port: 51235}, false, ident, make(chan Event, 1))
	p.setState(PeerStateConnected)
	caps := NewPeerCapabilities()
	if txrr {
		caps.Features.Enable(FeatureTxReduceRelay)
	}
	p.capabilities = caps
	return p
}

func gotFrame(p *Peer) bool { return len(p.send) > 0 }

// TestRelayTransaction_FeatureOff relays to every candidate peer and records
// no metrics when tx-reduce-relay is disabled (OverlayImpl.cpp:1251-1259 with
// TX_REDUCE_RELAY_ENABLE=false, metrics gate at 1255-1256).
func TestRelayTransaction_FeatureOff(t *testing.T) {
	ident, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:   Config{EnableTxReduceRelay: false},
		peers: make(map[PeerID]*Peer),
	}
	origin := relayTestPeer(t, ident, 1, true)
	o.peers[1] = origin
	for id := PeerID(2); id <= 4; id++ {
		o.peers[id] = relayTestPeer(t, ident, id, true)
	}

	o.RelayTransaction(1, []byte{0xAA})

	assert.False(t, gotFrame(origin), "origin must be excluded")
	for id := PeerID(2); id <= 4; id++ {
		assert.True(t, gotFrame(o.peers[id]), "peer %d should get the full frame", id)
	}
	assert.Zero(t, o.txm.selected.n, "no metrics recorded when feature and metrics flag are off")
}

// TestRelayTransaction_BelowMinRelaysToAll covers the path where the active
// peer count is at or below the minimum: relay to all candidates, and (since
// the feature is on) record selected=total, suppressed=1, notEnabled=0.
func TestRelayTransaction_BelowMinRelaysToAll(t *testing.T) {
	ident, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:   Config{EnableTxReduceRelay: true, TxReduceRelayMinPeers: 20, TxRelayPercentage: 25},
		peers: make(map[PeerID]*Peer),
	}
	o.peers[1] = relayTestPeer(t, ident, 1, true) // origin
	for id := PeerID(2); id <= 4; id++ {
		o.peers[id] = relayTestPeer(t, ident, id, true)
	}

	o.RelayTransaction(1, []byte{0xAA})

	for id := PeerID(2); id <= 4; id++ {
		assert.True(t, gotFrame(o.peers[id]), "peer %d should get the full frame", id)
	}
	assert.Equal(t, uint64(4), o.txm.selected.accum, "selected = total active peers")
	assert.Equal(t, uint64(1), o.txm.suppressed.accum, "suppressed = origin")
	assert.Equal(t, uint64(0), o.txm.notEnabled.accum)
}

// TestRelayTransaction_ReducePathSelectsSubset pins the reduce-relay
// selection (OverlayImpl.cpp:1261-1293): every disabled peer is relayed to in
// full, plus enabledTarget enabled peers; the rest are left for the
// HaveTransactions announce. With minPeers=2, pct=50, 1 enabled origin (skip),
// 4 enabled + 2 disabled candidates: total=7, disabled=2, minRelay=4,
// enabledTarget=3, enabledInSkip=1 → 2 disabled + 2 enabled relayed.
func TestRelayTransaction_ReducePathSelectsSubset(t *testing.T) {
	ident, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:   Config{EnableTxReduceRelay: true, TxReduceRelayMinPeers: 2, TxRelayPercentage: 50},
		peers: make(map[PeerID]*Peer),
	}
	o.peers[1] = relayTestPeer(t, ident, 1, true) // origin, enabled → enabledInSkip
	enabled := []PeerID{2, 3, 4, 5}
	disabled := []PeerID{6, 7}
	for _, id := range enabled {
		o.peers[id] = relayTestPeer(t, ident, id, true)
	}
	for _, id := range disabled {
		o.peers[id] = relayTestPeer(t, ident, id, false)
	}

	o.RelayTransaction(1, []byte{0xAA})

	disabledSent := 0
	for _, id := range disabled {
		if gotFrame(o.peers[id]) {
			disabledSent++
		}
	}
	enabledSent := 0
	for _, id := range enabled {
		if gotFrame(o.peers[id]) {
			enabledSent++
		}
	}

	assert.Equal(t, 2, disabledSent, "all disabled peers always get the full frame")
	assert.Equal(t, 2, enabledSent, "enabledTarget(3) - enabledInSkip(1) = 2 enabled peers relayed")
	assert.False(t, gotFrame(o.peers[1]), "origin excluded")

	assert.Equal(t, uint64(3), o.txm.selected.accum, "selected = enabledTarget")
	assert.Equal(t, uint64(1), o.txm.suppressed.accum, "suppressed = origin")
	assert.Equal(t, uint64(2), o.txm.notEnabled.accum, "notEnabled = disabled count")
}

// TestRecordInboundTxMetric_LedgerCategorisation pins that only the
// transaction-set-candidate variants of GET_LEDGER / LEDGER_DATA count toward
// the reduce-relay metrics, mirroring rippled's gl_tsc_*/ld_tsc_* gate
// (TrafficCount.cpp:64-106); general ledger-history sync is excluded.
func TestRecordInboundTxMetric_LedgerCategorisation(t *testing.T) {
	o := &Overlay{}

	tscGL, err := message.Encode(&message.GetLedger{
		InfoType: message.LedgerInfoTsCandidate, LType: message.LedgerTypeClosed,
	})
	require.NoError(t, err)
	baseGL, err := message.Encode(&message.GetLedger{
		InfoType: message.LedgerInfoBase, LType: message.LedgerTypeClosed,
	})
	require.NoError(t, err)

	o.recordInboundTxMetric(message.TypeGetLedger, tscGL, 100)
	assert.Equal(t, uint64(1), o.txm.getLedger.count.accum, "tx-set-candidate GET_LEDGER counted")
	o.recordInboundTxMetric(message.TypeGetLedger, baseGL, 100)
	assert.Equal(t, uint64(1), o.txm.getLedger.count.accum, "general GET_LEDGER not counted")

	tscLD, err := message.Encode(&message.LedgerData{
		LedgerHash: make([]byte, 32), LedgerSeq: 5, InfoType: message.LedgerInfoTsCandidate,
	})
	require.NoError(t, err)
	baseLD, err := message.Encode(&message.LedgerData{
		LedgerHash: make([]byte, 32), LedgerSeq: 5, InfoType: message.LedgerInfoBase,
	})
	require.NoError(t, err)

	o.recordInboundTxMetric(message.TypeLedgerData, tscLD, 100)
	assert.Equal(t, uint64(1), o.txm.ledgerData.count.accum, "tx-set-candidate LEDGER_DATA counted")
	o.recordInboundTxMetric(message.TypeLedgerData, baseLD, 100)
	assert.Equal(t, uint64(1), o.txm.ledgerData.count.accum, "general LEDGER_DATA not counted")
}
