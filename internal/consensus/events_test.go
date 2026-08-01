package consensus

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type eventSubscriberFunc func(Event)

func (f eventSubscriberFunc) OnEvent(event Event) {
	f(event)
}

// TestEventBus_DropsAreCounted verifies the event bus counts events shed when
// its buffer is full instead of losing them silently, so an operator debugging
// a gap on the validations/consensus streams has a signal.
func TestEventBus_DropsAreCounted(t *testing.T) {
	eb := NewEventBus(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var blockOnce sync.Once
	eb.Subscribe(eventSubscriberFunc(func(Event) {
		blockOnce.Do(func() {
			close(entered)
			<-release
		})
	}))
	if err := eb.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const total = 5
	if !eb.Publish(&TimerFiredEvent{Timer: TimerLedgerClose}) {
		t.Fatal("first Publish rejected")
	}
	<-entered
	if !eb.Publish(&TimerFiredEvent{Timer: TimerLedgerClose}) {
		t.Fatal("buffered Publish rejected")
	}
	for range total - 2 {
		if eb.Publish(&TimerFiredEvent{Timer: TimerLedgerClose}) {
			t.Fatal("Publish succeeded with a full buffer")
		}
	}
	close(release)
	eb.Stop()

	if got, want := eb.DroppedEvents(), uint64(total-2); got != want {
		t.Errorf("DroppedEvents = %d, want %d", got, want)
	}
}

func TestEventBus_OrderedFanOut(t *testing.T) {
	eb := NewEventBus(4)
	var mu sync.Mutex
	var got []string
	for _, name := range []string{"a", "b"} {
		eb.Subscribe(eventSubscriberFunc(func(event Event) {
			round := event.(*RoundStartedEvent).Round.Seq
			mu.Lock()
			got = append(got, fmt.Sprintf("%s%d", name, round))
			mu.Unlock()
		}))
	}
	if err := eb.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for seq := uint32(1); seq <= 3; seq++ {
		if !eb.Publish(&RoundStartedEvent{Round: RoundID{Seq: seq}}) {
			t.Fatalf("Publish(%d) rejected", seq)
		}
	}
	eb.Stop()

	want := []string{"a1", "b1", "a2", "b2", "a3", "b3"}
	if !slices.Equal(got, want) {
		t.Fatalf("delivery order = %v, want %v", got, want)
	}
}

func TestEventBus_OneShotLifecycle(t *testing.T) {
	eb := NewEventBus(4)
	if eb.Publish(&TimerFiredEvent{}) {
		t.Fatal("Publish before Start was accepted")
	}
	if err := eb.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := eb.Start(); !errors.Is(err, ErrEventBusStarted) {
		t.Fatalf("second Start error = %v, want %v", err, ErrEventBusStarted)
	}
	eb.Stop()
	eb.Stop()
	if err := eb.Start(); !errors.Is(err, ErrEventBusStopped) {
		t.Fatalf("Start after Stop error = %v, want %v", err, ErrEventBusStopped)
	}
}

func TestEventBus_StopWaitsForCallback(t *testing.T) {
	eb := NewEventBus(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	eb.Subscribe(eventSubscriberFunc(func(Event) {
		close(entered)
		<-release
	}))
	if err := eb.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !eb.Publish(&TimerFiredEvent{}) {
		t.Fatal("Publish rejected")
	}
	<-entered

	stopped := make(chan struct{})
	go func() {
		eb.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while callback was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not join the worker")
	}
	if eb.Publish(&TimerFiredEvent{}) {
		t.Fatal("Publish after Stop was accepted")
	}
}

func TestEventBus_LateSubscription(t *testing.T) {
	eb := NewEventBus(1)
	if err := eb.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var delivered atomic.Int32
	eb.Subscribe(eventSubscriberFunc(func(Event) {
		delivered.Add(1)
	}))
	if !eb.Publish(&TimerFiredEvent{}) {
		t.Fatal("Publish rejected")
	}
	eb.Stop()
	if got := delivered.Load(); got != 1 {
		t.Fatalf("delivered = %d, want 1", got)
	}
}

func TestEventBus_ConcurrentPublishAndStop(t *testing.T) {
	eb := NewEventBus(64)
	var delivered atomic.Int32
	eb.Subscribe(eventSubscriberFunc(func(Event) {
		delivered.Add(1)
	}))
	if err := eb.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := make(chan struct{})
	var accepted atomic.Int32
	var publishers sync.WaitGroup
	for range 64 {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			<-start
			if eb.Publish(&TimerFiredEvent{}) {
				accepted.Add(1)
			}
		}()
	}
	stopped := make(chan struct{})
	go func() {
		<-start
		eb.Stop()
		close(stopped)
	}()
	close(start)
	publishers.Wait()
	<-stopped

	if got, want := delivered.Load(), accepted.Load(); got != want {
		t.Fatalf("delivered = %d, accepted = %d", got, want)
	}
}
