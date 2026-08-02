package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

type processSignalError struct {
	signal os.Signal
}

func (e *processSignalError) Error() string {
	return fmt.Sprintf("received %s", e.signal)
}

type processSignals struct {
	ctx    context.Context
	reload <-chan os.Signal
	stop   func()
}

type processSignalSource func(context.Context) processSignals

func systemProcessSignals(parent context.Context) processSignals {
	return subscribeProcessSignals(parent, signal.Notify, signal.Stop)
}

func subscribeProcessSignals(
	parent context.Context,
	notify func(chan<- os.Signal, ...os.Signal),
	stop func(chan<- os.Signal),
) processSignals {
	termination := make(chan os.Signal, 1)
	reload := make(chan os.Signal, 1)
	notify(termination, syscall.SIGINT, syscall.SIGTERM)
	notify(reload, syscall.SIGHUP)

	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case received := <-termination:
			cancel(&processSignalError{signal: received})
		case <-ctx.Done():
		}
	}()

	var stopOnce sync.Once
	return processSignals{
		ctx:    ctx,
		reload: reload,
		stop: func() {
			stopOnce.Do(func() {
				stop(termination)
				stop(reload)
				cancel(nil)
				<-done
			})
		},
	}
}

func isProcessSignal(err error) bool {
	var signalErr *processSignalError
	return errors.As(err, &signalErr)
}

func wrapsOnly(err, target error) bool {
	if err == nil || err == target {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !wrapsOnly(child, target) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return wrapsOnly(wrapped.Unwrap(), target)
	}
	return false
}
