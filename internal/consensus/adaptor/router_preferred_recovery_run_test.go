package adaptor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

const preferredRecoveryBudgetThreshold = 16 << 20

// routerPreferredRecoveryBudget blocks only the small detached-preparation
// reservation. The normal dirty workspace reservation remains available so
// the fixture and unrelated router work can finish while recovery waits.
type routerPreferredRecoveryBudget struct {
	mu       sync.Mutex
	limit    uint64
	used     uint64
	blocked  bool
	changed  chan struct{}
	attempts atomic.Uint64
}

func newRouterPreferredRecoveryBudget(limit uint64) *routerPreferredRecoveryBudget {
	return &routerPreferredRecoveryBudget{limit: limit, changed: make(chan struct{})}
}

func (b *routerPreferredRecoveryBudget) TryReserve(bytes uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked && bytes < preferredRecoveryBudgetThreshold {
		b.attempts.Add(1)
		return false
	}
	if bytes > b.limit-b.used {
		return false
	}
	b.used += bytes
	return true
}

func (b *routerPreferredRecoveryBudget) Release(bytes uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.used {
		panic("preferred recovery budget underflow")
	}
	b.used -= bytes
	b.signalLocked()
}

func (b *routerPreferredRecoveryBudget) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed
}

func (b *routerPreferredRecoveryBudget) Limit() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit
}

func (b *routerPreferredRecoveryBudget) Block() {
	b.mu.Lock()
	b.blocked = true
	b.mu.Unlock()
}

func (b *routerPreferredRecoveryBudget) Unblock() {
	b.mu.Lock()
	b.blocked = false
	b.signalLocked()
	b.mu.Unlock()
}

func (b *routerPreferredRecoveryBudget) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func TestRouterRun_PreferredRecoveryKeepsIngressResponsive(t *testing.T) {
	budget := newRouterPreferredRecoveryBudget(2 * shamap.DirtyWorkspaceBytes)
	svc, err := service.New(service.Config{
		Standalone:        true,
		GenesisConfig:     genesis.DefaultConfig(),
		PersistenceMemory: budget,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	a, _ := newRecordingAdaptor(t, svc)
	a.SetOperatingMode(consensus.OpModeConnected)

	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	e := rcl.NewEngine(a, cfg)
	require.NoError(t, e.Start(t.Context()))

	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	require.NoError(t, e.StartRound(consensus.RoundID{
		Seq:        parent.Sequence() + 1,
		ParentHash: consensus.LedgerID(parent.Hash()),
	}, false))
	_, target, _, _ := buildSuccessorAgainstParent(t, parent)
	state, err := target.StateMapSnapshot()
	require.NoError(t, err)
	tx, err := target.TxMapSnapshot()
	require.NoError(t, err)
	header := target.Header()
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &header, state, tx))

	inbox := make(chan *peermanagement.InboundMessage, 8)
	router := newTestRouter(e, a, inbox)
	routerCtx, cancelRouter := context.WithCancel(t.Context())
	routerDone := make(chan struct{})
	go func() {
		router.Run(routerCtx)
		close(routerDone)
	}()

	t.Cleanup(func() {
		budget.Unblock()
		cancelRouter()
		select {
		case <-routerDone:
		case <-time.After(time.Second):
			t.Errorf("Router.Run did not stop")
		}
		require.NoError(t, e.Stop())
		require.NoError(t, a.StopLedgerAccept())
		svc.Stop()
	})

	a.UpdatePeerLCL(1, consensus.LedgerID(target.Hash()))
	a.UpdatePeerLCL(2, consensus.LedgerID(target.Hash()))
	budget.Block()
	e.TimerEntry()
	require.Eventually(t, func() bool {
		return budget.attempts.Load() > 0
	}, time.Second, time.Millisecond,
		"preferred recovery must wait on the detached preparation budget")

	heartbeatDone := make(chan struct{})
	go func() {
		e.TimerEntry()
		close(heartbeatDone)
	}()
	select {
	case <-heartbeatDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("heartbeat blocked behind preferred preparation")
	}

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	inbox <- statusChangeMessage(t, 3, closed.Sequence(), closed.Hash())
	require.Eventually(t, func() bool {
		for _, id := range a.PeerReportedLedgers() {
			if id == consensus.LedgerID(closed.Hash()) {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond, "Router.Run must process peer control while recovery waits")

	validation := &consensus.Validation{
		Full:      true,
		LedgerSeq: target.Sequence(),
		LedgerID:  consensus.LedgerID(target.Hash()),
		SignTime:  a.Now(),
		SeenTime:  a.Now(),
	}
	require.NoError(t, a.SignValidation(validation))
	inbox <- &peermanagement.InboundMessage{
		PeerID:  4,
		Type:    message.TypeValidation,
		Payload: encodePayload(t, &message.Validation{Validation: serializeSTValidation(validation)}),
	}
	require.Eventually(t, func() bool {
		a.mu.Lock()
		historian := a.validationHistorian
		a.mu.Unlock()
		if historian == nil {
			return false
		}
		return len(historian.GetTrustedFullValidations(
			consensus.LedgerID(target.Hash()), target.Sequence(),
		)) > 0
	}, time.Second, time.Millisecond, "trusted validation must be ingested while recovery waits")

	budget.Unblock()
	require.Eventually(t, func() bool {
		current := svc.GetClosedLedger()
		return current != nil && current.Hash() == target.Hash() && e.Mode() == consensus.ModeSwitchedLedger
	}, time.Second, 5*time.Millisecond, "preferred recovery must complete after the budget is released")
}
