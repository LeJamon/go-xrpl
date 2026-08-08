package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestLoadCostEarlyErrorTiming(t *testing.T) {
	tests := []struct {
		name    string
		handler types.MethodHandler
		params  json.RawMessage
		want    int
	}{
		{"sign", &SignMethod{}, json.RawMessage(`{}`), resource.FeeHeavyBurdenRPC().Cost()},
		{"sign_for", &SignForMethod{}, json.RawMessage(`{}`), resource.FeeHeavyBurdenRPC().Cost()},
		{"submit_multisigned", &SubmitMultisignedMethod{}, json.RawMessage(`{}`), resource.FeeHeavyBurdenRPC().Cost()},
		{"submit", &SubmitMethod{}, json.RawMessage(`{}`), resource.FeeMediumBurdenRPC().Cost()},
		{"simulate", &SimulateMethod{}, json.RawMessage(`{"binary":"invalid"}`), resource.FeeMediumBurdenRPC().Cost()},
		{"account_tx", &AccountTxMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"gateway_balances", &GatewayBalancesMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"account_lines", &AccountLinesMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"account_objects", &AccountObjectsMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"account_offers", &AccountOffersMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"account_channels", &AccountChannelsMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"account_nfts", &AccountNftsMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"ledger_data", &LedgerDataMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
		{"noripple_check", &NoRippleCheckMethod{}, json.RawMessage(`{}`), resource.FeeReferenceRPC().Cost()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:  context.Background(),
				LoadCost: uint32(resource.FeeReferenceRPC().Cost()),
				Services: &types.ServiceContainer{Capabilities: types.RPCCapabilities{SigningEnabled: true}},
			}
			_, rpcErr := test.handler.Handle(ctx, test.params)
			if rpcErr == nil {
				t.Fatal("expected request to fail before normal completion")
			}
			if got := int(ctx.LoadCost); got != test.want {
				t.Fatalf("load cost = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRipplePathFindBusyAdmissionIsHeavy(t *testing.T) {
	shedder := types.NewClientLoadShedder()
	for range types.MaxPathfindClients + 1 {
		shedder.Begin()
	}
	ctx := &types.RpcContext{
		Context:  context.Background(),
		LoadCost: uint32(resource.FeeReferenceRPC().Cost()),
		Services: &types.ServiceContainer{
			Ledger:       &loadAdmissionLedger{serverInfo: &types.LedgerServerInfo{Standalone: true}},
			ClientLoad:   shedder,
			Capabilities: types.RPCCapabilities{PathSearchMax: 3},
		},
	}

	_, rpcErr := (&ripplePathFindMethod{}).Handle(ctx, json.RawMessage(`{}`))
	if rpcErr == nil {
		t.Fatal("expected busy path-find request to fail admission")
	}
	if got := int(ctx.LoadCost); got != resource.FeeHeavyBurdenRPC().Cost() {
		t.Fatalf("load cost = %d, want %d", got, resource.FeeHeavyBurdenRPC().Cost())
	}
}
