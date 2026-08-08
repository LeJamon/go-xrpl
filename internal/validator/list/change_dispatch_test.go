package list

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestChangeDispatcherReentryPanicAndOrder(t *testing.T) {
	var d changeDispatcher
	var mu sync.Mutex
	var order []int
	var agg Aggregator
	agg.logger = slog.Default()
	for i := 1; i <= 3; i++ {
		d.enqueue(changeEvent{callback: func(_ []consensus.NodeID, _ [][33]byte) {
			if i == 1 {
				_ = agg.PublisherCount()
				panic("test panic")
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		}})
	}
	d.drain(agg.logger)
	if got, want := order, []int{2, 3}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("callback order after panic: got %v want %v", got, want)
	}
}

func TestChangeDispatcherSlowCallbackDoesNotBlockAggregator(t *testing.T) {
	agg := &Aggregator{}
	entered := make(chan struct{})
	release := make(chan struct{})
	agg.changes.enqueue(changeEvent{callback: func(_ []consensus.NodeID, _ [][33]byte) {
		close(entered)
		<-release
	}})
	done := make(chan struct{})
	go func() {
		agg.dispatchChanges()
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	snapshotDone := make(chan struct{})
	go func() {
		_ = agg.PublisherCount()
		close(snapshotDone)
	}()
	select {
	case <-snapshotDone:
	case <-time.After(time.Second):
		t.Fatal("aggregator method blocked behind callback")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not finish")
	}
}

func TestChangeDispatcherRetainsPayloadAndClonesDelivery(t *testing.T) {
	var d changeDispatcher
	got := make(chan [33]byte, 1)
	var key [33]byte
	key[0] = 1
	validators := []consensus.NodeID{{1}}
	masters := [][33]byte{key}
	d.enqueue(changeEvent{callback: func(v []consensus.NodeID, m [][33]byte) {
		v[0][0] = 9
		got <- m[0]
	}, validators: validators, masterKeys: masters})
	validators[0][0] = 8
	masters[0][0] = 7
	d.drain(nil)
	if gotKey := <-got; gotKey[0] != 1 {
		t.Fatalf("callback did not receive retained event payload: got %d", gotKey[0])
	}
	if validators[0][0] != 8 || masters[0][0] != 7 {
		t.Fatal("delivery mutation escaped event ownership")
	}
}
