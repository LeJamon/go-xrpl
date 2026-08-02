package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestWrapsOnlyDistinguishesShutdownFailure(t *testing.T) {
	cause := &processSignalError{signal: os.Interrupt}
	if !wrapsOnly(fmt.Errorf("startup: %w", cause), cause) {
		t.Fatal("single wrapped signal should be normalized")
	}
	if wrapsOnly(errors.Join(cause, errors.New("shutdown failed")), cause) {
		t.Fatal("signal must not hide a shutdown failure")
	}
}

func TestSubscribeProcessSignalsOwnsAndReleasesRegistrations(t *testing.T) {
	t.Parallel()

	type registration struct {
		channel chan<- os.Signal
		signals []os.Signal
	}
	var mu sync.Mutex
	var registrations []registration
	var stopped []chan<- os.Signal
	subscription := subscribeProcessSignals(
		context.Background(),
		func(ch chan<- os.Signal, signals ...os.Signal) {
			mu.Lock()
			defer mu.Unlock()
			registrations = append(registrations, registration{channel: ch, signals: signals})
		},
		func(ch chan<- os.Signal) {
			mu.Lock()
			defer mu.Unlock()
			stopped = append(stopped, ch)
		},
	)

	mu.Lock()
	if len(registrations) != 2 {
		t.Fatalf("registrations = %d, want 2", len(registrations))
	}
	termination := registrations[0]
	reload := registrations[1]
	mu.Unlock()
	if cap(termination.channel) != 1 || cap(reload.channel) != 1 {
		t.Fatal("process signal channels must coalesce with capacity 1")
	}
	if len(termination.signals) != 2 || termination.signals[0] != syscall.SIGINT || termination.signals[1] != syscall.SIGTERM {
		t.Fatalf("termination signals = %v, want SIGINT and SIGTERM", termination.signals)
	}
	if len(reload.signals) != 1 || reload.signals[0] != syscall.SIGHUP {
		t.Fatalf("reload signals = %v, want SIGHUP", reload.signals)
	}

	reload.channel <- syscall.SIGHUP
	select {
	case signal := <-subscription.reload:
		if signal != syscall.SIGHUP {
			t.Fatalf("reload signal = %v, want SIGHUP", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("reload signal was not delivered")
	}
	termination.channel <- syscall.SIGTERM
	select {
	case <-subscription.ctx.Done():
		if !isProcessSignal(context.Cause(subscription.ctx)) {
			t.Fatalf("context cause = %v, want process signal", context.Cause(subscription.ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("termination signal did not cancel process context")
	}

	subscription.stop()
	subscription.stop()
	mu.Lock()
	defer mu.Unlock()
	if len(stopped) != 2 {
		t.Fatalf("stopped registrations = %d, want 2", len(stopped))
	}
}
