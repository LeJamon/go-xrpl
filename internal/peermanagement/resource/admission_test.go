package resource

import (
	"sync"
	"testing"
)

func TestAdmissionBoundsConcurrentWorkPerEndpoint(t *testing.T) {
	clock := newFakeClock()
	limits := DefaultLimits()
	limits.MaxInflightPerConsumer = 2
	m := NewManagerWithLimits(clock.Now, nil, limits)
	consumer := m.NewInboundEndpoint("192.0.2.1:5000")
	if consumer == nil {
		t.Fatal("consumer acquisition failed")
	}
	defer consumer.Release()

	first, result := consumer.Admit(FeeHeavyBurdenRPC())
	if first == nil || result != Ok {
		t.Fatalf("first admission = (%v, %v), want admitted", first, result)
	}
	second, result := consumer.Admit(FeeHeavyBurdenRPC())
	if second == nil || result != Ok {
		t.Fatalf("second admission = (%v, %v), want admitted", second, result)
	}
	if third, result := consumer.Admit(FeeHeavyBurdenRPC()); third != nil || result != Drop {
		t.Fatalf("third admission = (%v, %v), want bounded rejection", third, result)
	}

	other := m.NewInboundEndpoint("192.0.2.2:5000")
	if other == nil {
		t.Fatal("independent consumer acquisition failed")
	}
	defer other.Release()
	independent, result := other.Admit(FeeHeavyBurdenRPC())
	if independent == nil || result != Ok {
		t.Fatalf("independent admission = (%v, %v), want admitted", independent, result)
	}

	first.Finish(FeeHeavyBurdenRPC(), "first")
	second.Finish(FeeReferenceRPC(), "second")
	independent.Cancel()
	if stats := m.Stats(); stats.Inflight != 0 || stats.InflightRejections != 1 {
		t.Fatalf("stats after completion = %+v", stats)
	}
}

func TestAdmissionFinishReconcilesExactlyOnce(t *testing.T) {
	m, _ := newTestManager()
	consumer := m.NewInboundEndpoint("198.51.100.8")
	if consumer == nil {
		t.Fatal("consumer acquisition failed")
	}
	admission, result := consumer.Admit(FeeHeavyBurdenRPC())
	if admission == nil || result != Ok {
		t.Fatalf("admission = (%v, %v), want admitted", admission, result)
	}
	consumer.Release()

	const callers = 32
	results := make(chan Completion, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- admission.Finish(FeeMediumBurdenRPC(), "test")
		}()
	}
	wg.Wait()
	close(results)

	var first Completion
	for result := range results {
		if first == (Completion{}) {
			first = result
		}
		if result != first {
			t.Fatalf("completion changed across callers: first=%+v got=%+v", first, result)
		}
	}
	if stats := m.Stats(); stats.Inflight != 0 {
		t.Fatalf("inflight after repeated finish = %d", stats.Inflight)
	}
	reacquired := m.NewInboundEndpoint("198.51.100.8:6000")
	if reacquired == nil {
		t.Fatal("reacquisition failed")
	}
	defer reacquired.Release()
	if got, want := reacquired.Balance(), int64(FeeMediumBurdenRPC().Cost()/DecayWindowSeconds); got != want {
		t.Fatalf("balance after repeated finish = %d, want %d", got, want)
	}
}

func TestConsumerConcurrentReleaseLeavesSharedHandleActive(t *testing.T) {
	m, _ := newTestManager()
	first := m.NewInboundEndpoint("203.0.113.20:5000")
	second := m.NewInboundEndpoint("203.0.113.20:6000")
	if first == nil || second == nil {
		t.Fatal("consumer acquisition failed")
	}
	defer second.Release()

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first.Charge(FeeReferenceRPC(), "race")
			_ = first.Balance()
			_ = first.Disposition()
			_ = first.Disconnect()
			first.Release()
		}()
	}
	wg.Wait()

	if result := second.Charge(FeeReferenceRPC(), "shared"); result != Ok {
		t.Fatalf("shared handle charge = %v, want Ok", result)
	}
	if second.Balance() <= 0 {
		t.Fatal("shared handle lost the endpoint reputation")
	}
}

func TestAdmissionConcurrentSameKeyIsBoundedAndDistinctKeysRemainIndependent(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxInflightPerConsumer = 4
	m := NewManagerWithLimits(nil, nil, limits)
	consumer := m.NewInboundEndpoint("192.0.2.30")
	if consumer == nil {
		t.Fatal("consumer acquisition failed")
	}
	defer consumer.Release()

	const callers = 64
	start := make(chan struct{})
	results := make(chan *Admission, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			admission, _ := consumer.Admit(FeeHeavyBurdenRPC())
			results <- admission
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	admitted := 0
	for admission := range results {
		if admission != nil {
			admitted++
			admission.Cancel()
		}
	}
	if admitted != limits.MaxInflightPerConsumer {
		t.Fatalf("same-key admissions = %d, want %d", admitted, limits.MaxInflightPerConsumer)
	}

	for _, address := range []string{"192.0.2.31", "192.0.2.32"} {
		other := m.NewInboundEndpoint(address)
		if other == nil {
			t.Fatalf("consumer %s acquisition failed", address)
		}
		admission, result := other.Admit(FeeHeavyBurdenRPC())
		if admission == nil || result != Ok {
			t.Fatalf("distinct-key admission %s = (%v, %v), want admitted", address, admission, result)
		}
		admission.Cancel()
		other.Release()
	}
	if stats := m.Stats(); stats.Inflight != 0 || stats.InflightRejections != uint64(callers-limits.MaxInflightPerConsumer) {
		t.Fatalf("concurrent admission stats = %+v", stats)
	}
}
