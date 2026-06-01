package handlers

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func unlimitedCtx(s *types.ClientLoadShedder) *types.RpcContext {
	return &types.RpcContext{
		Role:      types.RoleAdmin,
		Unlimited: true,
		Services:  &types.ServiceContainer{ClientLoad: s},
	}
}

func gatedCtx(s *types.ClientLoadShedder) *types.RpcContext {
	return &types.RpcContext{Services: &types.ServiceContainer{ClientLoad: s}}
}

func loadInFlight(s *types.ClientLoadShedder, n int64) {
	for i := int64(0); i < n; i++ {
		s.Begin()
	}
}

func TestGates_NilOrUnwiredIsNoOp(t *testing.T) {
	for _, ctx := range []*types.RpcContext{
		nil,
		{},
		{Services: &types.ServiceContainer{}},
	} {
		if rpcErr := RequireNotBusyClient(ctx); rpcErr != nil {
			t.Fatalf("RequireNotBusyClient(%v) = %v, want nil", ctx, rpcErr)
		}
		if rpcErr := RequireNotBusyBookOffers(ctx); rpcErr != nil {
			t.Fatalf("RequireNotBusyBookOffers(%v) = %v, want nil", ctx, rpcErr)
		}
		release, rpcErr := AcquirePathfind(ctx)
		if rpcErr != nil {
			t.Fatalf("AcquirePathfind(%v) = %v, want nil", ctx, rpcErr)
		}
		if release == nil {
			t.Fatalf("AcquirePathfind(%v) returned nil release", ctx)
		}
		release()
	}
}

// Strict-greater semantics for the generic gate mirror
// rippled RPCHandler.cpp:135 (maxJobQueueClients = 500).
func TestRequireNotBusyClient_Strictness(t *testing.T) {
	s := types.NewClientLoadShedder()
	loadInFlight(s, types.MaxJobQueueClients)

	if rpcErr := RequireNotBusyClient(gatedCtx(s)); rpcErr != nil {
		t.Fatalf("count==500 should not shed, got %v", rpcErr)
	}
	s.Begin()
	rpcErr := RequireNotBusyClient(gatedCtx(s))
	if rpcErr == nil {
		t.Fatal("count==501 should shed")
	}
	if rpcErr.Code != types.RpcTOO_BUSY || rpcErr.ErrorString != "tooBusy" {
		t.Errorf("got code=%d errorString=%q, want %d/%q", rpcErr.Code, rpcErr.ErrorString, types.RpcTOO_BUSY, "tooBusy")
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

	if rpcErr := RequireNotBusyBookOffers(gatedCtx(s)); rpcErr != nil {
		t.Fatalf("count==200 should not shed, got %v", rpcErr)
	}
	s.Begin()
	if rpcErr := RequireNotBusyBookOffers(gatedCtx(s)); rpcErr == nil {
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
	if rpcErr := RequireNotBusyBookOffers(unlimitedCtx(s)); rpcErr != nil {
		t.Fatalf("admin must bypass book_offers gate, got %v", rpcErr)
	}
	release, rpcErr := AcquirePathfind(unlimitedCtx(s))
	if rpcErr != nil {
		t.Fatalf("admin must bypass pathfind gate, got %v", rpcErr)
	}
	release() // must be a safe no-op
}

// AcquirePathfind mirrors LegacyPathFind ctor (LegacyPathFind.cpp:30-60):
// the first gate is the > maxPathfindJobCount (50) check.
func TestAcquirePathfind_JobCountGate(t *testing.T) {
	s := types.NewClientLoadShedder()
	loadInFlight(s, types.MaxPathfindClients) // == 50

	release, rpcErr := AcquirePathfind(gatedCtx(s))
	if rpcErr != nil {
		t.Fatalf("count==50 should not shed, got %v", rpcErr)
	}
	release()

	s.Begin() // 51
	if _, rpcErr := AcquirePathfind(gatedCtx(s)); rpcErr == nil {
		t.Fatal("count==51 should shed before reaching in-progress check")
	}
	if got := s.PathfindActive(); got != 0 {
		t.Errorf("pathfindActive leaked: got %d, want 0", got)
	}
}

// AcquirePathfind enforces the concurrent-in-progress cap from
// rippled LegacyPathFind.cpp:47 (maxPathfindsInProgress = 2).
func TestAcquirePathfind_InProgressCap(t *testing.T) {
	s := types.NewClientLoadShedder()

	r1, err1 := AcquirePathfind(gatedCtx(s))
	if err1 != nil {
		t.Fatalf("first acquire should succeed: %v", err1)
	}
	r2, err2 := AcquirePathfind(gatedCtx(s))
	if err2 != nil {
		t.Fatalf("second acquire should succeed: %v", err2)
	}

	if _, err3 := AcquirePathfind(gatedCtx(s)); err3 == nil {
		t.Fatal("third concurrent acquire must shed (cap = 2)")
	}
	if got := s.PathfindActive(); got != types.MaxPathfindsInProgress {
		t.Errorf("PathfindActive = %d, want %d", got, types.MaxPathfindsInProgress)
	}

	r1()
	r3, err3 := AcquirePathfind(gatedCtx(s))
	if err3 != nil {
		t.Fatalf("after release a slot should free up: %v", err3)
	}
	r2()
	r3()
	if got := s.PathfindActive(); got != 0 {
		t.Errorf("PathfindActive leaked after all release: %d", got)
	}
}
