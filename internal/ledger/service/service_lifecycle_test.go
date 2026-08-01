package service

import (
	"testing"
	"time"
)

func TestServiceLifecycle_StopBeforeStartRejectsRestart(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	svc.Stop()
	if err := svc.Start(); err == nil {
		t.Fatal("Start succeeded after Stop")
	}

	done := make(chan struct{})
	go func() {
		svc.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Stop blocked")
	}
}

func TestServiceLifecycle_StartAndStopAreIdempotent(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	var stopped [2]chan struct{}
	for i := range stopped {
		stopped[i] = make(chan struct{})
		go func(done chan struct{}) {
			svc.Stop()
			close(done)
		}(stopped[i])
	}
	for i, done := range stopped {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Stop %d blocked", i)
		}
	}
	if err := svc.Start(); err == nil {
		t.Fatal("Start succeeded after service shutdown")
	}
}

func TestServiceLifecycle_StartupFailureDoesNotArmWorkers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GenesisConfig.CloseTimeResolution = 11
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Start(); err == nil {
		t.Fatal("Start accepted invalid genesis resolution")
	}
	if svc.persistStarted {
		t.Fatal("persistence worker started before startup completed")
	}
	if svc.ledgerEventStarted {
		t.Fatal("event publisher started before startup completed")
	}
	if err := svc.Start(); err == nil {
		t.Fatal("Start retried a failed partial initialization")
	}
	svc.Stop()
}

func TestServiceLifecycle_EventSinkStartDuringStopReturns(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	tryStart := make(chan struct{})
	startErr := make(chan error, 1)
	setEventSinkFunc(svc, func(*LedgerAcceptedEvent) {
		close(entered)
		<-tryStart
		startErr <- svc.Start()
	})
	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{LedgerInfo: &LedgerInfo{Sequence: 1}})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("event sink was not invoked")
	}

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		svc.lifecycleMu.Lock()
		stopping := svc.lifecycleState == serviceStopping
		svc.lifecycleMu.Unlock()
		if stopping {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not enter stopping state")
		}
		time.Sleep(time.Millisecond)
	}
	close(tryStart)

	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("Start succeeded while Stop was in progress")
		}
	case <-time.After(time.Second):
		t.Fatal("Start from event sink blocked during Stop")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete after event sink returned")
	}
}
