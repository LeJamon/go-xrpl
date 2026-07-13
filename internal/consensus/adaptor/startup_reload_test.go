package adaptor

import (
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestComponentsValidateValidatorReload(t *testing.T) {
	var publisherA [33]byte
	publisherA[0] = 0xED
	var publisherB [33]byte
	publisherB[0] = 0x02

	staticOnly := &Components{}
	if err := staticOnly.ValidateValidatorReload(nil, nil, 0, 1); err != nil {
		t.Fatalf("static trust rejected: %v", err)
	}
	if err := staticOnly.ValidateValidatorReload(nil, nil, 0, 0); err == nil {
		t.Fatal("empty trust reload accepted")
	}

	published := &Components{
		configuredPublisherKeys:      [][33]byte{publisherA},
		configuredPublisherSites:     []string{"https://vl.example"},
		configuredPublisherThreshold: 1,
	}
	if err := published.ValidateValidatorReload([][33]byte{publisherA}, []string{"https://vl.example"}, 1, 0); err != nil {
		t.Fatalf("unchanged publisher trust rejected: %v", err)
	}
	if err := published.ValidateValidatorReload([][33]byte{publisherB}, []string{"https://vl.example"}, 1, 0); err == nil {
		t.Fatal("publisher key change accepted without restart")
	}
	if err := published.ValidateValidatorReload([][33]byte{publisherA}, []string{"https://other.example"}, 1, 0); err == nil {
		t.Fatal("publisher site change accepted without restart")
	}
	if err := published.ValidateValidatorReload([][33]byte{publisherA}, []string{"https://vl.example"}, 2, 0); err == nil {
		t.Fatal("publisher threshold change accepted without restart")
	}
}

func TestComponentsSerializesStaticAndPublisherTrustUpdates(t *testing.T) {
	masterKey := func(seed byte) [33]byte {
		var key [33]byte
		key[0] = 0xED
		key[32] = seed
		return key
	}

	oldPublisherMaster := masterKey(1)
	newPublisherMaster := masterKey(2)
	staticMaster := masterKey(3)
	oldPublisher := consensus.CalcNodeID(oldPublisherMaster)
	newPublisher := consensus.CalcNodeID(newPublisherMaster)
	staticValidator := consensus.CalcNodeID(staticMaster)

	a := New(Config{})
	c := &Components{Adaptor: a}
	c.updatePublisherTrust(
		[]consensus.NodeID{oldPublisher},
		[][33]byte{oldPublisherMaster},
	)

	entered := make(chan struct{})
	release := make(chan struct{})
	var blockNext atomic.Bool
	a.OnTrustChanged(func([]consensus.NodeID, int) {
		if blockNext.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	})
	blockNext.Store(true)

	reloadDone := make(chan struct{})
	go func() {
		c.ReloadStaticValidators(
			[]consensus.NodeID{staticValidator},
			[][33]byte{staticMaster},
		)
		close(reloadDone)
	}()
	<-entered

	if c.trustMergeMu.TryLock() {
		c.trustMergeMu.Unlock()
		close(release)
		t.Fatal("trust merge mutex was released before the adaptor update completed")
	}

	publisherDone := make(chan struct{})
	publisherObservedContention := make(chan bool)
	go func() {
		if c.trustMergeMu.TryLock() {
			c.trustMergeMu.Unlock()
			publisherObservedContention <- false
			close(publisherDone)
			return
		}
		publisherObservedContention <- true
		c.updatePublisherTrust(
			[]consensus.NodeID{newPublisher},
			[][33]byte{newPublisherMaster},
		)
		close(publisherDone)
	}()
	if !<-publisherObservedContention {
		close(release)
		<-reloadDone
		<-publisherDone
		t.Fatal("publisher update did not contend with the in-flight reload")
	}
	close(release)
	<-reloadDone
	<-publisherDone

	trusted := make(map[consensus.NodeID]struct{})
	for _, node := range a.GetTrustedValidators() {
		trusted[node] = struct{}{}
	}
	if _, ok := trusted[staticValidator]; !ok {
		t.Fatal("static validator was lost during concurrent publisher update")
	}
	if _, ok := trusted[newPublisher]; !ok {
		t.Fatal("new publisher validator was overwritten by stale reload state")
	}
	if _, ok := trusted[oldPublisher]; ok {
		t.Fatal("stale publisher validator remained trusted")
	}
}
