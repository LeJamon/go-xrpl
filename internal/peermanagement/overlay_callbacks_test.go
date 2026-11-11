package peermanagement

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPeerLifecycleCallbacksConcurrentAccess guards the providersMu
// discipline added for onPeerConnect/onPeerDisconnect: a higher layer
// re-wiring the callbacks must not race the event-loop goroutine that
// reads and fires them. Run with -race to catch a regression.
func TestPeerLifecycleCallbacksConcurrentAccess(t *testing.T) {
	o := &Overlay{}

	const iterations = 2000
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range iterations {
			o.SetPeerConnectCallback(func(PeerID) {})
			o.SetPeerDisconnectCallback(func(PeerID) {})
		}
	}()

	go func() {
		defer wg.Done()
		for range iterations {
			if cb := o.onPeerConnectSnapshot(); cb != nil {
				cb(PeerID(0))
			}
			if cb := o.onPeerDisconnectSnapshot(); cb != nil {
				cb(PeerID(0))
			}
		}
	}()

	wg.Wait()
}

// TestPeerLifecycleCallbacksSetAndClear verifies the setter/snapshot
// roundtrip: wiring delivers the peer id, and passing nil clears it.
func TestPeerLifecycleCallbacksSetAndClear(t *testing.T) {
	o := &Overlay{}

	if cb := o.onPeerConnectSnapshot(); cb != nil {
		t.Fatal("expected nil connect callback before wiring")
	}
	if cb := o.onPeerDisconnectSnapshot(); cb != nil {
		t.Fatal("expected nil disconnect callback before wiring")
	}

	var gotConnect, gotDisconnect PeerID
	o.SetPeerConnectCallback(func(id PeerID) { gotConnect = id })
	o.SetPeerDisconnectCallback(func(id PeerID) { gotDisconnect = id })

	if cb := o.onPeerConnectSnapshot(); cb != nil {
		cb(PeerID(7))
	} else {
		t.Fatal("expected connect callback after wiring")
	}
	if cb := o.onPeerDisconnectSnapshot(); cb != nil {
		cb(PeerID(9))
	} else {
		t.Fatal("expected disconnect callback after wiring")
	}
	if gotConnect != 7 {
		t.Errorf("connect callback got id %d, want 7", gotConnect)
	}
	if gotDisconnect != 9 {
		t.Errorf("disconnect callback got id %d, want 9", gotDisconnect)
	}

	o.SetPeerConnectCallback(nil)
	o.SetPeerDisconnectCallback(nil)
	if cb := o.onPeerConnectSnapshot(); cb != nil {
		t.Error("expected nil connect callback after clearing")
	}
	if cb := o.onPeerDisconnectSnapshot(); cb != nil {
		t.Error("expected nil disconnect callback after clearing")
	}
}

func TestNewInstallsConstructionHooks(t *testing.T) {
	var ledgerHints, validLedger, txRecords, nodeObjects, clusterFee, localLoadFee bool
	inbound := &testMemoryAccount{limit: 1}
	outbound := &testMemoryAccount{limit: 2}
	critical := &testMemoryAccount{limit: 3}
	o, err := New(
		WithListenAddr("127.0.0.1:0"),
		WithLedgerHintProvider(func() (LedgerHints, bool) { ledgerHints = true; return LedgerHints{}, false }),
		WithValidLedgerProvider(func() (uint32, time.Duration, bool) { validLedger = true; return 0, 0, false }),
		WithTxRecordProvider(func([32]byte) (TxRecord, bool) { txRecords = true; return TxRecord{}, false }),
		WithNodeObjectProvider(func([32]byte) ([]byte, bool) { nodeObjects = true; return nil, false }),
		WithClusterFeeSink(func(uint32) { clusterFee = true }),
		WithLocalLoadFeeProvider(func() (uint32, time.Duration) { localLoadFee = true; return 0, 0 }),
		WithMemoryBudgets(inbound, outbound, critical),
	)
	require.NoError(t, err)

	o.ledgerHintProvider()
	o.validLedgerProvider()
	o.txRecordProvider([32]byte{})
	o.nodeObjectProvider([32]byte{})
	o.clusterFeeSink(0)
	o.localLoadFeeProvider()
	require.True(t, ledgerHints && validLedger && txRecords && nodeObjects && clusterFee && localLoadFee)
	require.Same(t, inbound, o.inboundReadBudget.parent)
	require.Same(t, outbound, o.outboundBudget.parent)
	require.Same(t, critical, o.outboundBudget.criticalParent)
}

func TestNewLeavesConstructionHooksUnsetByDefault(t *testing.T) {
	o, err := New(WithListenAddr("127.0.0.1:0"))
	require.NoError(t, err)

	require.Nil(t, o.ledgerHintProvider)
	require.Nil(t, o.validLedgerProvider)
	require.Nil(t, o.txRecordProvider)
	require.Nil(t, o.nodeObjectProvider)
	require.Nil(t, o.clusterFeeSink)
	require.Nil(t, o.localLoadFeeProvider)
	require.Nil(t, o.inboundReadBudget.parent)
	require.Nil(t, o.outboundBudget.parent)
	require.Nil(t, o.outboundBudget.criticalParent)
}
