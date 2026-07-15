package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestLoadCostEarlyErrorTiming(t *testing.T) {
	tests := []struct {
		name    string
		handler types.MethodHandler
		params  json.RawMessage
		want    loadtrack.LoadKind
	}{
		{"sign", &SignMethod{}, json.RawMessage(`{}`), loadtrack.LoadHeavy},
		{"sign_for", &SignForMethod{}, json.RawMessage(`{}`), loadtrack.LoadHeavy},
		{"submit_multisigned", &SubmitMultisignedMethod{}, json.RawMessage(`{}`), loadtrack.LoadHeavy},
		{"submit", &SubmitMethod{}, json.RawMessage(`{}`), loadtrack.LoadMedium},
		{"simulate", &SimulateMethod{}, json.RawMessage(`{"binary":"invalid"}`), loadtrack.LoadMedium},
		{"account_tx", &AccountTxMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"gateway_balances", &GatewayBalancesMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"account_lines", &AccountLinesMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"account_objects", &AccountObjectsMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"account_offers", &AccountOffersMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"account_channels", &AccountChannelsMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"account_nfts", &AccountNftsMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"ledger_data", &LedgerDataMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
		{"noripple_check", &NoRippleCheckMethod{}, json.RawMessage(`{}`), loadtrack.LoadReference},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:  context.Background(),
				LoadCost: uint32(loadtrack.LoadReference),
			}
			_, rpcErr := test.handler.Handle(ctx, test.params)
			if rpcErr == nil {
				t.Fatal("expected request to fail before normal completion")
			}
			if got := loadtrack.LoadKind(ctx.LoadCost); got != test.want {
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
		LoadCost: uint32(loadtrack.LoadReference),
		Services: &types.ServiceContainer{ClientLoad: shedder},
	}

	_, rpcErr := (&RipplePathFindMethod{}).Handle(ctx, json.RawMessage(`{}`))
	if rpcErr == nil {
		t.Fatal("expected busy path-find request to fail admission")
	}
	if got := loadtrack.LoadKind(ctx.LoadCost); got != loadtrack.LoadHeavy {
		t.Fatalf("load cost = %d, want %d", got, loadtrack.LoadHeavy)
	}
}
