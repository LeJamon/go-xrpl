package csf

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

func TestSchedulerAtStepUntilAndStepWhile(t *testing.T) {
	scheduler := NewScheduler()
	var order []int
	scheduler.At(SimTime(time.Second), func() { order = append(order, 1) })
	scheduler.At(SimTime(2*time.Second), func() { order = append(order, 2) })
	scheduler.At(SimTime(3*time.Second), func() { order = append(order, 3) })
	cancel := scheduler.At(SimTime(4*time.Second), func() { order = append(order, 4) })
	cancel()

	if got := scheduler.StepUntil(SimTime(1500 * time.Millisecond)); got != 1 {
		t.Fatalf("StepUntil processed %d events, want 1", got)
	}
	if !slices.Equal(order, []int{1}) {
		t.Fatalf("order after StepUntil = %v, want [1]", order)
	}
	if got := scheduler.Now(); got != SimTime(1500*time.Millisecond) {
		t.Fatalf("Now after StepUntil = %v, want 1.5s", got)
	}

	scheduler.At(scheduler.Now(), func() { order = append(order, 15) })
	if got := scheduler.StepUntil(scheduler.Now()); got != 1 {
		t.Fatalf("StepUntil(now) processed %d events, want 1", got)
	}
	if got := scheduler.StepWhile(func() bool { return len(order) < 3 }); got != 1 {
		t.Fatalf("StepWhile processed %d events, want 1", got)
	}
	if !slices.Equal(order, []int{1, 15, 2}) {
		t.Fatalf("order after StepWhile = %v, want [1 15 2]", order)
	}
	if got := scheduler.StepWhile(func() bool { return false }); got != 0 {
		t.Fatalf("false StepWhile processed %d events", got)
	}
	if got := scheduler.PendingCount(); got != 1 {
		t.Fatalf("pending after StepWhile = %d, want 1", got)
	}

	if got := scheduler.StepUntil(SimTime(5 * time.Second)); got != 1 {
		t.Fatalf("final StepUntil processed %d events, want 1", got)
	}
	if !slices.Equal(order, []int{1, 15, 2, 3}) {
		t.Fatalf("final order = %v, want [1 15 2 3]", order)
	}
	if got := scheduler.Now(); got != SimTime(5*time.Second) {
		t.Fatalf("final Now = %v, want 5s", got)
	}
}

func TestSchedulerSerializesReadyDrivers(t *testing.T) {
	scheduler := NewScheduler()
	firstStarted := make(chan struct{})
	secondAttempted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRan := make(chan struct{})

	scheduler.In(time.Second, func() {
		<-secondAttempted
		close(firstStarted)
		<-releaseFirst
	})
	scheduler.In(2*time.Second, func() { close(secondRan) })

	firstDone := make(chan struct{})
	go func() {
		scheduler.StepOne()
		close(firstDone)
	}()

	secondDone := make(chan struct{})
	go func() {
		close(secondAttempted)
		scheduler.StepOne()
		close(secondDone)
	}()

	<-firstStarted
	select {
	case <-secondRan:
		t.Fatal("second driver ran while the first handler was active")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	<-firstDone
	<-secondDone
	select {
	case <-secondRan:
	default:
		t.Fatal("second driver returned without processing its event")
	}
	if got := scheduler.Now(); got != SimTime(2*time.Second) {
		t.Fatalf("Now = %v, want 2s", got)
	}
}

func TestSchedulerNowTimeUsesWholeSeconds(t *testing.T) {
	scheduler := NewScheduler()
	epoch := time.Unix(protocol.RippleEpochUnix, 0).UTC().Add(24 * time.Hour)

	scheduler.StepUntil(SimTime(1500 * time.Millisecond))
	if got, want := scheduler.NowTime(), epoch.Add(time.Second); got != want {
		t.Fatalf("NowTime at 1.5s = %v, want %v", got, want)
	}
	scheduler.StepUntil(SimTime(1999 * time.Millisecond))
	if got, want := scheduler.NowTime(), epoch.Add(time.Second); got != want {
		t.Fatalf("NowTime at 1.999s = %v, want %v", got, want)
	}
	scheduler.StepUntil(SimTime(2 * time.Second))
	if got, want := scheduler.NowTime(), epoch.Add(2*time.Second); got != want {
		t.Fatalf("NowTime at 2s = %v, want %v", got, want)
	}
}

func TestSimRunZeroDrainsScheduledWork(t *testing.T) {
	sim := NewSim()
	runs := 0
	sim.Scheduler.In(time.Second, func() { runs++ })

	if err := sim.Run(0); err != nil {
		t.Fatalf("Run(0): %v", err)
	}
	if runs != 1 {
		t.Fatalf("scheduled callbacks = %d, want 1", runs)
	}
	if !sim.Scheduler.Empty() {
		t.Fatalf("Run(0) left %d callbacks queued", sim.Scheduler.PendingCount())
	}
}

func TestBasicNetworkRejectsNegativeDelayWithoutLocking(t *testing.T) {
	scheduler := NewScheduler()
	network := NewBasicNetwork(scheduler)
	if network.Connect(1, 2, -time.Nanosecond) {
		t.Fatal("negative-delay connection succeeded")
	}
	if network.IsConnected(1, 2) {
		t.Fatal("negative-delay connection changed topology")
	}
	if !network.Connect(1, 2, time.Nanosecond) {
		t.Fatal("valid connection failed after negative-delay rejection")
	}
}

func TestBasicNetworkSendUnlocksAfterSchedulerPanic(t *testing.T) {
	scheduler := NewScheduler()
	scheduler.StepUntil(SimTime(math.MaxInt64 - 1))
	network := NewBasicNetwork(scheduler)
	if !network.Connect(1, 2, 2*time.Nanosecond) {
		t.Fatal("connect failed")
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Send did not panic on scheduler time overflow")
			}
		}()
		network.Send(1, 2, func() {})
	}()

	if !network.mu.TryLock() {
		t.Fatal("Send left the network mutex locked after scheduler panic")
	}
	network.mu.Unlock()
}

func TestBasicNetworkSameTimeReconnectKeepsSentMessages(t *testing.T) {
	scheduler := NewScheduler()
	network := NewBasicNetwork(scheduler)
	if !network.Connect(1, 2, 2*time.Second) {
		t.Fatal("initial connect failed")
	}

	var delivered []string
	network.Send(1, 2, func() { delivered = append(delivered, "old") })
	network.Disconnect(1, 2)
	if !network.Connect(1, 2, 2*time.Second) {
		t.Fatal("reconnect failed")
	}
	network.Send(1, 2, func() { delivered = append(delivered, "new") })

	if got := scheduler.PendingCount(); got != 2 {
		t.Fatalf("pending after reconnect = %d, want both generations queued", got)
	}
	if !scheduler.StepOne() {
		t.Fatal("old delivery was canceled instead of consuming time")
	}
	if !slices.Equal(delivered, []string{"old"}) {
		t.Fatalf("same-time reconnect deliveries = %v, want [old]", delivered)
	}
	if got := scheduler.Now(); got != SimTime(2*time.Second) {
		t.Fatalf("Now after old generation = %v, want 2s", got)
	}
	if !scheduler.StepOne() || !slices.Equal(delivered, []string{"old", "new"}) {
		t.Fatalf("deliveries = %v, want [old new]", delivered)
	}
}

func TestBasicNetworkLaterReconnectDropsEarlierMessages(t *testing.T) {
	scheduler := NewScheduler()
	network := NewBasicNetwork(scheduler)
	if !network.Connect(1, 2, 2*time.Second) {
		t.Fatal("initial connect failed")
	}

	delivered := false
	network.Send(1, 2, func() { delivered = true })
	scheduler.In(time.Second, func() {
		network.Disconnect(1, 2)
		if !network.Connect(1, 2, 2*time.Second) {
			t.Fatal("reconnect failed")
		}
	})
	scheduler.Step()
	if delivered {
		t.Fatal("message sent before a later reconnection was delivered")
	}
}

func TestBasicNetworkDisconnectAllCancelsRetiredDeliveries(t *testing.T) {
	scheduler := NewScheduler()
	network := NewBasicNetwork(scheduler)
	network.Connect(1, 2, time.Second)
	network.Send(1, 2, func() { t.Fatal("retired delivery ran after teardown") })
	network.Disconnect(1, 2)
	network.DisconnectAll()
	if !scheduler.Empty() {
		t.Fatalf("scheduler retained %d network deliveries after teardown", scheduler.PendingCount())
	}
}

func TestLedgerOracleTreatsZeroCloseTimeAsDisagreement(t *testing.T) {
	oracle := NewLedgerOracle()
	genesis := oracle.Genesis()

	ledger := oracle.Accept(genesis, NewTxSet(), time.Time{}, true, 30*time.Second)
	if ledger.CloseAgree() {
		t.Fatal("zero close time recorded close-time agreement")
	}
	if got, want := ledger.CloseTime(), genesis.CloseTime().Add(time.Second); got != want {
		t.Fatalf("zero close time = %v, want %v", got, want)
	}
}
