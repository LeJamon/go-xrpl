package service

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicationDispatcherOrdersProposedBeforeValidated(t *testing.T) {
	svc := &Service{}
	publisher := &eventPublisher{
		service:               svc,
		publicationLimit:      4,
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	proposedStarted := make(chan struct{})
	releaseProposed := make(chan struct{})
	validated := make(chan struct{})
	var mu sync.Mutex
	var order []string
	publisher.setSubmittedTxCallback(func(SubmittedTxEvent) {
		mu.Lock()
		order = append(order, "proposed")
		mu.Unlock()
		close(proposedStarted)
		<-releaseProposed
	})
	publisher.setEventSink(EventSinkFunc(func(*LedgerAcceptedEvent) error {
		mu.Lock()
		order = append(order, "validated")
		mu.Unlock()
		close(validated)
		return nil
	}))
	publisher.start()

	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{})
	select {
	case <-proposedStarted:
	case <-time.After(time.Second):
		t.Fatal("proposed publication did not start")
	}
	publisher.dispatchLedgerEvent(&LedgerAcceptedEvent{})
	select {
	case <-validated:
		t.Fatal("validated publication overtook proposed publication")
	default:
	}
	close(releaseProposed)
	select {
	case <-validated:
	case <-time.After(time.Second):
		t.Fatal("validated publication did not follow proposed publication")
	}
	publisher.stop()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "proposed" || order[1] != "validated" {
		t.Fatalf("publication order = %v, want [proposed validated]", order)
	}
}

func TestPublicationDispatcherPreservesProposalBurstOrder(t *testing.T) {
	publisher := &eventPublisher{
		service:               &Service{},
		publicationLimit:      16,
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	done := make(chan struct{})
	var mu sync.Mutex
	var order []uint32
	publisher.setSubmittedTxCallback(func(event SubmittedTxEvent) {
		mu.Lock()
		order = append(order, event.CurrentLedger)
		if len(order) == 10 {
			close(done)
		}
		mu.Unlock()
	})
	publisher.start()
	for i := uint32(0); i < 10; i++ {
		publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: i})
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proposal burst was not delivered")
	}
	publisher.stop()

	mu.Lock()
	defer mu.Unlock()
	for i := range order {
		if order[i] != uint32(i) {
			t.Fatalf("proposal order = %v", order)
		}
	}
}

func TestPublicationDispatcherFailsClosedAtCapacity(t *testing.T) {
	publisher := &eventPublisher{
		service:               &Service{},
		publicationLimit:      1,
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var delivered []uint32
	publisher.setSubmittedTxCallback(func(event SubmittedTxEvent) {
		delivered = append(delivered, event.CurrentLedger)
		if event.CurrentLedger == 1 {
			close(started)
			<-release
		}
	})
	publisher.start()
	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: 1})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first publication did not start")
	}
	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: 2})
	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: 3})
	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: 4})

	select {
	case err := <-publisher.publicationErrors:
		if err == nil || !strings.Contains(err.Error(), "capacity 1") {
			t.Fatalf("publication failure = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue capacity failure was not reported")
	}
	publisher.ledgerEventMu.Lock()
	queued := len(publisher.publicationQueue)
	failed := publisher.publicationFailed
	publisher.ledgerEventMu.Unlock()
	if queued != 1 || !failed {
		t.Fatalf("queue state = len %d, failed %t; want len 1 and failed", queued, failed)
	}

	close(release)
	publisher.stop()
	if len(delivered) != 2 || delivered[0] != 1 || delivered[1] != 2 {
		t.Fatalf("delivered events = %v, want accepted prefix [1 2]", delivered)
	}
}

func TestPublicationDispatcherFailsClosedOnSinkError(t *testing.T) {
	sinkErr := errors.New("corrupt accepted transaction")
	publisher := &eventPublisher{
		service:               &Service{},
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	publisher.setEventSink(EventSinkFunc(func(*LedgerAcceptedEvent) error {
		return sinkErr
	}))
	publisher.start()
	publisher.dispatchLedgerEvent(&LedgerAcceptedEvent{})

	select {
	case err := <-publisher.publicationErrors:
		if !errors.Is(err, sinkErr) {
			t.Fatalf("publication failure = %v, want %v", err, sinkErr)
		}
	case <-time.After(time.Second):
		t.Fatal("sink failure was not reported")
	}

	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: 1})
	publisher.ledgerEventMu.Lock()
	queued := len(publisher.publicationQueue)
	failed := publisher.publicationFailed
	publisher.ledgerEventMu.Unlock()
	if queued != 0 || !failed {
		t.Fatalf("queue state after sink failure = len %d, failed %t", queued, failed)
	}
	publisher.stop()
}

func TestPublicationDispatcherRejectsAfterStop(t *testing.T) {
	publisher := &eventPublisher{
		service:               &Service{},
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	called := make(chan struct{}, 1)
	publisher.setSubmittedTxCallback(func(SubmittedTxEvent) { called <- struct{}{} })
	publisher.start()
	publisher.stop()
	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{})
	select {
	case <-called:
		t.Fatal("publication callback ran after stop")
	default:
	}
}

func TestServerStatusSignalPreservesPublicationOrder(t *testing.T) {
	publisher := &eventPublisher{
		service:               &Service{},
		publicationLimit:      4,
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var mu sync.Mutex
	var order []string
	publisher.setSubmittedTxCallback(func(event SubmittedTxEvent) {
		mu.Lock()
		order = append(order, "proposed")
		mu.Unlock()
		if event.CurrentLedger == 1 {
			close(started)
			<-release
		}
		if event.CurrentLedger == 2 {
			close(done)
		}
	})
	publisher.setServerStatusCallback(func(*string) {
		mu.Lock()
		order = append(order, "server")
		mu.Unlock()
	})
	publisher.start()
	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: 1})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first publication did not start")
	}
	if !publisher.dispatchServerStatusEvent() {
		t.Fatal("server status signal was rejected")
	}
	publisher.dispatchSubmittedTxEvent(SubmittedTxEvent{CurrentLedger: 2})
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publication queue did not drain")
	}
	publisher.stop()

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(order, ","); got != "proposed,server,proposed" {
		t.Fatalf("publication order = %s", got)
	}
}

func TestServerStatusSignalCoalescesAndAllowsCallbackReentry(t *testing.T) {
	publisher := &eventPublisher{
		service:               &Service{},
		publicationLimit:      2,
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	thirdDone := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	publisher.setServerStatusCallback(func(*string) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		switch call {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case 2:
			if !publisher.dispatchServerStatusEvent() {
				t.Error("reentrant status signal was rejected")
			}
		case 3:
			close(thirdDone)
		}
	})
	publisher.start()
	if !publisher.dispatchServerStatusEvent() {
		t.Fatal("first status signal was rejected")
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first status callback did not start")
	}
	for range 10 {
		if !publisher.dispatchServerStatusEvent() {
			t.Fatal("coalesced status signal was rejected")
		}
	}
	close(releaseFirst)
	select {
	case <-thirdDone:
	case <-time.After(time.Second):
		t.Fatal("reentrant status callback did not run")
	}
	publisher.stop()

	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("status callbacks = %d, want 3", calls)
	}
}

func TestServerStatusSignalDrainsOnStopAndRejectsAfter(t *testing.T) {
	publisher := &eventPublisher{
		service:               &Service{},
		publicationLimit:      2,
		publicationErrors:     make(chan error, 1),
		ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	drained := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	publisher.setServerStatusCallback(func(*string) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(started)
			<-release
		}
	})
	publisher.start()
	publisher.dispatchServerStatusEvent()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("status callback did not start")
	}
	publisher.dispatchServerStatusEvent()
	go func() {
		publisher.stop()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("stop returned before queued status drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("stop did not return after status drain")
	}
	if publisher.dispatchServerStatusEvent() {
		t.Fatal("status signal was accepted after stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("status callbacks = %d, want 2", calls)
	}
}
