package handlers

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// TestRequireNotBusy_NilServicesIsNoOp ensures RequireNotBusy tolerates an
// RpcContext built without a service container — the path taken by many
// existing unit tests. A nil services / nil ClientLoad must never fire the
// rpcTOO_BUSY gate.
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

// TestRequireNotBusy_BelowThreshold confirms a freshly-constructed shedder
// at zero in-flight is below threshold and lets the request through.
func TestRequireNotBusy_BelowThreshold(t *testing.T) {
	shedder := types.NewClientLoadShedder(types.DefaultClientLoadShedThreshold)
	ctx := &types.RpcContext{Services: &types.ServiceContainer{ClientLoad: shedder}}

	if rpcErr := RequireNotBusy(ctx); rpcErr != nil {
		t.Fatalf("idle shedder rejected with %v", rpcErr)
	}
}

// TestRequireNotBusy_AtThresholdStillOK pins the strict-greater semantics
// of rippled's `getJobCountGE(jtCLIENT) > 200`: 200 in-flight is exactly at
// the ceiling and must not shed yet.
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

// TestRequireNotBusy_OverThresholdShed exercises the rippled-parity gate:
// once one more dispatch starts (threshold+1), RequireNotBusy returns
// rpcTOO_BUSY with code 9 and the rippled error_message.
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

// TestRequireNotBusy_RecoversAfterEnd verifies the in-flight counter decays
// as End() is paired with each Begin(), so transient bursts don't latch the
// shedder open.
func TestRequireNotBusy_RecoversAfterEnd(t *testing.T) {
	shedder := types.NewClientLoadShedder(types.DefaultClientLoadShedThreshold)
	for i := int64(0); i <= types.DefaultClientLoadShedThreshold; i++ {
		shedder.Begin()
	}
	ctx := &types.RpcContext{Services: &types.ServiceContainer{ClientLoad: shedder}}
	if rpcErr := RequireNotBusy(ctx); rpcErr == nil {
		t.Fatal("expected shed at threshold+1")
	}

	// Drain back to threshold: one End() drops below the strict-greater
	// boundary.
	shedder.End()

	if rpcErr := RequireNotBusy(ctx); rpcErr != nil {
		t.Fatalf("after drain expected pass, got %v", rpcErr)
	}
}

// TestClientLoadShedder_LowThresholdAlwaysSheds covers the test-harness
// shortcut of using a non-positive threshold to force the busy path. With
// threshold = 0 a single in-flight request must trip the gate.
func TestClientLoadShedder_LowThresholdAlwaysSheds(t *testing.T) {
	shedder := types.NewClientLoadShedder(0)
	shedder.Begin()
	if !shedder.ShouldShed() {
		t.Fatal("threshold=0, in-flight=1 should shed")
	}
}
