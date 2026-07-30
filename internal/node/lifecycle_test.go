package node

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

func TestRunReturnsCanceledContextBeforeStartup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("startup canceled")
	cancel(cause)

	err := Run(ctx, nil, "", false, service.StartupConfig{}, xrpllog.Discard(), xrpllog.Discard())
	if !errors.Is(err, cause) {
		t.Fatalf("Run() error = %v, want %v", err, cause)
	}
}

func TestWaitForShutdownReturnsContextCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("auxiliary listener failed")
	result := make(chan error, 1)
	go func() {
		result <- waitForShutdown(
			ctx,
			xrpllog.Discard(),
			make(chan os.Signal),
			make(chan os.Signal),
			make(chan struct{}),
			make(chan error),
			nil,
			nil,
			"",
		)
	}()

	cancel(cause)
	select {
	case err := <-result:
		if !errors.Is(err, cause) {
			t.Fatalf("waitForShutdown() error = %v, want %v", err, cause)
		}
	case <-time.After(time.Second):
		t.Fatal("waitForShutdown() did not return after context cancellation")
	}
}

func TestWaitForShutdownReturnsComponentError(t *testing.T) {
	t.Parallel()

	componentErr := errors.New("consensus router stopped")
	componentErrCh := make(chan error, 1)
	componentErrCh <- componentErr

	err := waitForShutdown(
		context.Background(),
		xrpllog.Discard(),
		make(chan os.Signal),
		make(chan os.Signal),
		make(chan struct{}),
		make(chan error),
		componentErrCh,
		nil,
		"",
	)
	if !errors.Is(err, componentErr) {
		t.Fatalf("waitForShutdown() error = %v, want %v", err, componentErr)
	}
}
