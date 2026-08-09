package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func unlimitedCtx(s *types.ClientLoadShedder) *types.RpcContext {
	return &types.RpcContext{
		Role:     types.RoleAdmin,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{ClientLoad: s}),
	}
}

func gatedCtx(s *types.ClientLoadShedder) *types.RpcContext {
	return &types.RpcContext{Services: types.NewTestServiceGraph(&types.ServiceContainer{ClientLoad: s})}
}

func loadInFlight(s *types.ClientLoadShedder, n int64) {
	for range n {
		s.Begin()
	}
}

func TestGates_NilOrUnwiredIsNoOp(t *testing.T) {
	for _, ctx := range []*types.RpcContext{
		nil,
		{},
		{Services: types.NewTestServiceGraph(&types.ServiceContainer{})},
	} {
		if rpcErr := RequireNotBusyClient(ctx); rpcErr != nil {
			t.Fatalf("RequireNotBusyClient(%v) = %v, want nil", ctx, rpcErr)
		}
		if rpcErr := requireNotBusyBookOffers(ctx); rpcErr != nil {
			t.Fatalf("RequireNotBusyBookOffers(%v) = %v, want nil", ctx, rpcErr)
		}
		release, rpcErr := acquirePathfind(ctx)
		if rpcErr != nil {
			t.Fatalf("AcquirePathfind(%v) = %v, want nil", ctx, rpcErr)
		}
		if release == nil {
			t.Fatalf("AcquirePathfind(%v) returned nil release", ctx)
		}
		release()
	}
}

func TestRequireNotBusyClient_Strictness(t *testing.T) {
	s := types.NewClientLoadShedder()
	loadInFlight(s, types.MaxJobQueueClients-1)

	if rpcErr := RequireNotBusyClient(gatedCtx(s)); rpcErr != nil {
		t.Fatalf("count==499 should not shed, got %v", rpcErr)
	}
	s.Begin()
	rpcErr := RequireNotBusyClient(gatedCtx(s))
	if rpcErr == nil {
		t.Fatal("count==500 should shed")
	}
	if rpcErr.Code != rpcerrors.RpcTOO_BUSY || rpcErr.ErrorString != "tooBusy" {
		t.Errorf("got code=%d errorString=%q, want %d/%q", rpcErr.Code, rpcErr.ErrorString, rpcerrors.RpcTOO_BUSY, "tooBusy")
	}
	if rpcErr.Message != "The server is too busy to help you now." {
		t.Errorf("error_message = %q, want rippled-canonical", rpcErr.Message)
	}
}

// Strict-greater semantics for the BookOffers gate mirror
// rippled BookOffers.cpp:42 (`getJobCountGE(jtCLIENT) > 200`).
func TestRequireNotBusyBookOffers_Strictness(t *testing.T) {
	s := types.NewClientLoadShedder()
	loadInFlight(s, types.MaxBookOffersClients)

	if rpcErr := requireNotBusyBookOffers(gatedCtx(s)); rpcErr != nil {
		t.Fatalf("count==200 should not shed, got %v", rpcErr)
	}
	s.Begin()
	if rpcErr := requireNotBusyBookOffers(gatedCtx(s)); rpcErr == nil {
		t.Fatal("count==201 should shed")
	}
}

// All gates exempt unlimited (admin/identified) callers, mirroring
// rippled isUnlimited(role) carve-out (Role.cpp:124-128).
func TestGates_UnlimitedBypass(t *testing.T) {
	s := types.NewClientLoadShedder()
	loadInFlight(s, types.MaxJobQueueClients+10) // well past every threshold

	if rpcErr := RequireNotBusyClient(unlimitedCtx(s)); rpcErr != nil {
		t.Fatalf("admin must bypass generic gate, got %v", rpcErr)
	}
	if rpcErr := requireNotBusyBookOffers(unlimitedCtx(s)); rpcErr != nil {
		t.Fatalf("admin must bypass book_offers gate, got %v", rpcErr)
	}
	release, rpcErr := acquirePathfind(unlimitedCtx(s))
	if rpcErr != nil {
		t.Fatalf("admin must bypass pathfind gate, got %v", rpcErr)
	}
	if got := s.PathfindActive(); got != 1 {
		t.Fatalf("admin pathfind active = %d, want 1", got)
	}
	release()
	if got := s.PathfindActive(); got != 0 {
		t.Fatalf("admin pathfind active after release = %d, want 0", got)
	}
}

// acquirePathfind mirrors LegacyPathFind ctor (LegacyPathFind.cpp:30-60):
// the first gate is the > maxPathfindJobCount (50) check.
func TestAcquirePathfind_JobCountGate(t *testing.T) {
	s := types.NewClientLoadShedder()
	loadInFlight(s, types.MaxPathfindClients) // == 50

	release, rpcErr := acquirePathfind(gatedCtx(s))
	if rpcErr != nil {
		t.Fatalf("count==50 should not shed, got %v", rpcErr)
	}
	release()

	s.Begin() // 51
	if _, rpcErr := acquirePathfind(gatedCtx(s)); rpcErr == nil {
		t.Fatal("count==51 should shed before reaching in-progress check")
	}
	if got := s.PathfindActive(); got != 0 {
		t.Errorf("pathfindActive leaked: got %d, want 0", got)
	}
}

func TestAcquirePathfind_LocalLoadGateWithoutClientCounter(t *testing.T) {
	ctx := &types.RpcContext{Services: types.NewTestServiceGraph(&types.ServiceContainer{
		IsLoadedLocal: func() bool { return true },
	})}
	if _, rpcErr := acquirePathfind(ctx); rpcErr == nil || rpcErr.ErrorString != "tooBusy" {
		t.Fatalf("local load should shed without ClientLoad, got %v", rpcErr)
	}
}

func TestAcquirePathfind_UnlimitedBypassesLocalLoad(t *testing.T) {
	s := types.NewClientLoadShedder()
	ctx := &types.RpcContext{
		Role: types.RoleAdmin,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			ClientLoad:    s,
			IsLoadedLocal: func() bool { return true },
		}),
	}
	release, rpcErr := acquirePathfind(ctx)
	if rpcErr != nil {
		t.Fatalf("unlimited caller should bypass local load, got %v", rpcErr)
	}
	if got := s.PathfindActive(); got != 1 {
		t.Fatalf("unlimited pathfind active = %d, want 1", got)
	}
	release()
}

func TestAcquirePathfind_UnlimitedExceedsCapButConsumesSlot(t *testing.T) {
	s := types.NewClientLoadShedder()
	r1, rpcErr := acquirePathfind(gatedCtx(s))
	if rpcErr != nil {
		t.Fatalf("first regular acquire: %v", rpcErr)
	}
	r2, rpcErr := acquirePathfind(gatedCtx(s))
	if rpcErr != nil {
		t.Fatalf("second regular acquire: %v", rpcErr)
	}
	adminRelease, rpcErr := acquirePathfind(unlimitedCtx(s))
	if rpcErr != nil {
		t.Fatalf("admin acquire above cap: %v", rpcErr)
	}
	if got := s.PathfindActive(); got != types.MaxPathfindsInProgress+1 {
		t.Fatalf("pathfind active with admin = %d, want %d", got, types.MaxPathfindsInProgress+1)
	}
	if _, rpcErr := acquirePathfind(gatedCtx(s)); rpcErr == nil {
		t.Fatal("regular caller should remain capped while admin is active")
	}
	adminRelease()
	r2()
	r1()
}

// acquirePathfind enforces the concurrent-in-progress cap from
// rippled LegacyPathFind.cpp:47 (maxPathfindsInProgress = 2).
func TestAcquirePathfind_InProgressCap(t *testing.T) {
	s := types.NewClientLoadShedder()

	r1, err1 := acquirePathfind(gatedCtx(s))
	if err1 != nil {
		t.Fatalf("first acquire should succeed: %v", err1)
	}
	r2, err2 := acquirePathfind(gatedCtx(s))
	if err2 != nil {
		t.Fatalf("second acquire should succeed: %v", err2)
	}

	if _, err3 := acquirePathfind(gatedCtx(s)); err3 == nil {
		t.Fatal("third concurrent acquire must shed (cap = 2)")
	}
	if got := s.PathfindActive(); got != types.MaxPathfindsInProgress {
		t.Errorf("PathfindActive = %d, want %d", got, types.MaxPathfindsInProgress)
	}

	r1()
	r3, err3 := acquirePathfind(gatedCtx(s))
	if err3 != nil {
		t.Fatalf("after release a slot should free up: %v", err3)
	}
	r2()
	r3()
	if got := s.PathfindActive(); got != 0 {
		t.Errorf("PathfindActive leaked after all release: %d", got)
	}
}

func TestWaitPathfindWaitsForRelease(t *testing.T) {
	s := types.NewClientLoadShedder()
	r1, err1 := acquirePathfind(gatedCtx(s))
	if err1 != nil {
		t.Fatalf("first acquire should succeed: %v", err1)
	}
	r2, err2 := acquirePathfind(gatedCtx(s))
	if err2 != nil {
		t.Fatalf("second acquire should succeed: %v", err2)
	}

	type waitResult struct {
		release func()
		ok      bool
	}
	acquired := make(chan waitResult, 1)
	go func() {
		release, ok := s.WaitPathfind(context.Background())
		acquired <- waitResult{release: release, ok: ok}
	}()

	select {
	case <-acquired:
		t.Fatal("third pathfind should wait while both slots are occupied")
	case <-time.After(10 * time.Millisecond):
	}

	r1()
	select {
	case result := <-acquired:
		if !result.ok {
			t.Fatal("waiting pathfind should acquire the released slot")
		}
		result.release()
	case <-time.After(time.Second):
		t.Fatal("waiting pathfind did not acquire the released slot")
	}

	r2()
	if got := s.PathfindActive(); got != 0 {
		t.Fatalf("PathfindActive leaked after wait: %d", got)
	}
}

func TestWaitPathfindHonorsCancellation(t *testing.T) {
	s := types.NewClientLoadShedder()
	r1 := s.AcquirePathfindUnlimited()
	r2 := s.AcquirePathfindUnlimited()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, ok := s.WaitPathfind(ctx); ok {
		t.Fatal("canceled pathfind wait should fail")
	}
	r1()
	r2()
}
