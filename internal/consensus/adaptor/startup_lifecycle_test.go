package adaptor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
)

type lifecycleTestEngine struct {
	mockEngine
	startErr  error
	stopCount atomic.Int32
	stopCheck func()
}

func (e *lifecycleTestEngine) Start(context.Context) error {
	return e.startErr
}

func (e *lifecycleTestEngine) Stop() error {
	if e.stopCheck != nil {
		e.stopCheck()
	}
	e.stopCount.Add(1)
	return nil
}

func newLifecycleTestComponents(
	t *testing.T,
	engine *lifecycleTestEngine,
	inbox <-chan *peermanagement.InboundMessage,
) *Components {
	t.Helper()
	overlay, err := peermanagement.New()
	require.NoError(t, err)
	adaptor := newTestAdaptor(t)
	if inbox == nil {
		inbox = overlay.ConsensusMessages()
	}
	return &Components{
		Overlay: overlay,
		Engine:  engine,
		Adaptor: adaptor,
		Router:  NewRouter(engine, adaptor, inbox),
	}
}

func TestComponentsStartEngineFailureRollsBack(t *testing.T) {
	startErr := errors.New("engine start failed")
	engine := &lifecycleTestEngine{startErr: startErr}
	components := newLifecycleTestComponents(t, engine, nil)

	err := components.Start(t.Context())
	require.ErrorIs(t, err, startErr)
	require.Equal(t, int32(1), engine.stopCount.Load())
	require.NotNil(t, components.overlayDone)
	select {
	case <-components.overlayDone:
	default:
		t.Fatal("Start returned before the overlay stopped")
	}

	components.Stop()
	require.Equal(t, int32(1), engine.stopCount.Load())
}

func TestComponentsReportsPostReadyOverlayExit(t *testing.T) {
	engine := &lifecycleTestEngine{}
	components := newLifecycleTestComponents(t, engine, nil)
	require.NoError(t, components.Start(t.Context()))

	require.NoError(t, components.Overlay.Stop())
	select {
	case err := <-components.Errors():
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "overlay stopped"), err.Error())
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected overlay exit was not reported")
	}

	components.Stop()
	select {
	case <-components.routerDone:
	default:
		t.Fatal("Stop returned before the router stopped")
	}
}

func TestComponentsReportsRouterExit(t *testing.T) {
	inbox := make(chan *peermanagement.InboundMessage)
	engine := &lifecycleTestEngine{}
	components := newLifecycleTestComponents(t, engine, inbox)
	require.NoError(t, components.Start(t.Context()))

	close(inbox)
	select {
	case err := <-components.Errors():
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "router stopped"), err.Error())
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected router exit was not reported")
	}

	components.Stop()
	select {
	case <-components.overlayDone:
	default:
		t.Fatal("Stop returned before the overlay stopped")
	}
}

func TestComponentsStopJoinsValidatorTickBeforeEngine(t *testing.T) {
	engine := &lifecycleTestEngine{}
	components := newLifecycleTestComponents(t, engine, nil)
	components.ValidatorList = stup_newAggregator(t)
	engine.stopCheck = func() {
		select {
		case <-components.vlTickDone:
		default:
			t.Error("engine stopped before ValidatorList tick exited")
		}
	}

	require.NoError(t, components.Start(t.Context()))
	require.NotNil(t, components.vlTickDone)
	components.Stop()
	require.Equal(t, int32(1), engine.stopCount.Load())
}

func TestComponentsStopJoinsRouterWorkBeforeEngine(t *testing.T) {
	engine := &lifecycleTestEngine{}
	components := newLifecycleTestComponents(t, engine, nil)
	require.NoError(t, components.Start(t.Context()))

	started := make(chan struct{})
	release := make(chan struct{})
	require.True(t, components.Router.runLifecycleTask(func(context.Context) {
		close(started)
		<-release
	}))
	<-started

	stopped := make(chan struct{})
	go func() {
		components.Stop()
		close(stopped)
	}()
	<-components.Router.lifecycleContext().Done()
	require.Zero(t, engine.stopCount.Load(), "engine stopped while Router work was active")

	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Components.Stop did not finish after Router work exited")
	}
	require.Equal(t, int32(1), engine.stopCount.Load())
}
