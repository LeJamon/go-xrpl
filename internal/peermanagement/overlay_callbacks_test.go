package peermanagement

import (
	"sync"
	"testing"
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

// TestProviderSettersConcurrentAccess extends the providersMu discipline to
// the tx-reduce-relay provider hooks: SetTxProvider/SetOpenLedgerHashesProvider
// must not race the inbound/outbound goroutines that read them via the
// snapshot accessors. Run with -race to catch a regression.
func TestProviderSettersConcurrentAccess(t *testing.T) {
	o := &Overlay{}

	const iterations = 2000
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range iterations {
			o.SetTxProvider(func([32]byte) ([]byte, bool) { return nil, false })
			o.SetOpenLedgerHashesProvider(func() [][32]byte { return nil })
		}
	}()

	go func() {
		defer wg.Done()
		for range iterations {
			if p := o.txProviderSnapshot(); p != nil {
				p([32]byte{})
			}
			if p := o.openLedgerHashesProviderSnapshot(); p != nil {
				p()
			}
		}
	}()

	wg.Wait()
}
