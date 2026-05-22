package handlers

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

func TestRequireNotBusy_NilServicesIsNoOp(t *testing.T) {
	for _, ctx := range []*types.RpcContext{
		nil,
		{},
		{Services: &types.ServiceContainer{}},
	} {
		if rpcErr := RequireNotBusy(ctx); rpcErr != nil {
			t.Fatalf("RequireNotBusy(%v) returned %v, want nil", ctx, rpcErr)
		}
	}
}

func TestRequireNotBusy_BelowThreshold(t *testing.T) {
	shedder := types.NewClientLoadShedder(types.DefaultClientLoadShedThreshold)
	ctx := &types.RpcContext{Services: &types.ServiceContainer{ClientLoad: shedder}}

	if rpcErr := RequireNotBusy(ctx); rpcErr != nil {
		t.Fatalf("idle shedder rejected with %v", rpcErr)
	}
}

// Pins strict-greater semantics of rippled's getJobCountGE(jtCLIENT) > 200
// (BookOffers.cpp:42): inFlight == threshold must not shed.
func TestRequireNotBusy_AtThresholdStillOK(t *testing.T) {
	shedder := types.NewClientLoadShedder(types.DefaultClientLoadShedThreshold)
	for i := int64(0); i < types.DefaultClientLoadShedThreshold; i++ {
		shedder.Begin()
	}
	ctx := &types.RpcContext{Services: &types.ServiceContainer{ClientLoad: shedder}}

	if rpcErr := RequireNotBusy(ctx); rpcErr != nil {
		t.Fatalf("count==threshold should not shed, got %v", rpcErr)
	}
}

func TestRequireNotBusy_OverThresholdShed(t *testing.T) {
	shedder := types.NewClientLoadShedder(types.DefaultClientLoadShedThreshold)
	for i := int64(0); i <= types.DefaultClientLoadShedThreshold; i++ {
		shedder.Begin()
	}
	ctx := &types.RpcContext{Services: &types.ServiceContainer{ClientLoad: shedder}}

	rpcErr := RequireNotBusy(ctx)
	if rpcErr == nil {
		t.Fatal("expected rpcTOO_BUSY, got nil")
	}
	if rpcErr.Code != types.RpcTOO_BUSY {
		t.Errorf("error code = %d, want %d (rpcTOO_BUSY)", rpcErr.Code, types.RpcTOO_BUSY)
	}
	if rpcErr.ErrorString != "tooBusy" {
		t.Errorf("error string = %q, want %q", rpcErr.ErrorString, "tooBusy")
	}
}

func TestRequireNotBusy_RecoversAfterEnd(t *testing.T) {
	shedder := types.NewClientLoadShedder(types.DefaultClientLoadShedThreshold)
	for i := int64(0); i <= types.DefaultClientLoadShedThreshold; i++ {
		shedder.Begin()
	}
	ctx := &types.RpcContext{Services: &types.ServiceContainer{ClientLoad: shedder}}
	if rpcErr := RequireNotBusy(ctx); rpcErr == nil {
		t.Fatal("expected shed at threshold+1")
	}

	shedder.End()

	if rpcErr := RequireNotBusy(ctx); rpcErr != nil {
		t.Fatalf("after drain expected pass, got %v", rpcErr)
	}
}

func TestClientLoadShedder_LowThresholdAlwaysSheds(t *testing.T) {
	shedder := types.NewClientLoadShedder(0)
	shedder.Begin()
	if !shedder.ShouldShed() {
		t.Fatal("threshold=0, in-flight=1 should shed")
	}
}
