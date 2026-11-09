package node

import (
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
)

// wirePeerHTTPProviders binds peer responses to live RPC service state.
func (r *nodeRuntime) wirePeerHTTPProviders(graph *types.ServiceGraph, peerSource types.PeerSource) {
	if r.consensus == nil || r.consensus.Overlay == nil || graph == nil {
		return
	}

	ctx := &types.RpcContext{
		Context:    r.ctx,
		Role:       types.RoleGuest,
		ApiVersion: types.DefaultApiVersion,
		PeerSource: peerSource,
		Services:   graph,
	}
	r.consensus.Overlay.SetHTTPProviders(peermanagement.HTTPProviders{
		ServerInfo: func() map[string]any {
			return peerServerInfo(ctx)
		},
		Counts: func() map[string]any {
			return peerServerCounts(ctx)
		},
		UNL: func() map[string]any {
			return peerUNLInfo(ctx)
		},
		ValidatorList: peerValidatorListProvider(r.consensus.ValidatorList),
	})
}

func peerServerInfo(ctx *types.RpcContext) map[string]any {
	result, rpcErr := (&handlers.ServerStateMethod{}).Handle(ctx, nil)
	if rpcErr != nil {
		return map[string]any{}
	}
	envelope, ok := result.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	state, ok := envelope["state"].(map[string]any)
	if !ok {
		return map[string]any{}
	}

	delete(state, "hostid")
	delete(state, "load_factor_fee_escalation")
	delete(state, "load_factor_fee_queue")
	delete(state, "validation_quorum")
	if ledger, ok := state["validated_ledger"].(map[string]any); ok {
		delete(ledger, "base_fee")
		delete(ledger, "reserve_base_xrp")
		delete(ledger, "reserve_inc_xrp")
	}
	return state
}

func peerServerCounts(ctx *types.RpcContext) map[string]any {
	result, rpcErr := (&handlers.GetCountsMethod{}).Handle(ctx, nil)
	if rpcErr != nil {
		return map[string]any{}
	}
	counts, ok := result.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return counts
}

func peerUNLInfo(ctx *types.RpcContext) map[string]any {
	result, rpcErr := (&handlers.ValidatorsMethod{}).Handle(ctx, nil)
	if rpcErr != nil {
		return map[string]any{}
	}
	unl, ok := result.(map[string]any)
	if !ok {
		return map[string]any{}
	}

	if publishers, ok := unl["publisher_lists"].([]map[string]any); ok {
		for _, publisher := range publishers {
			delete(publisher, "list")
		}
	}
	delete(unl, "signing_keys")
	delete(unl, "trusted_validator_keys")
	delete(unl, "validation_quorum")

	sites := make([]map[string]any, 0)
	if ctx != nil && ctx.Services != nil && ctx.Services.ValidatorList() != nil {
		for _, site := range ctx.Services.ValidatorList().Sites() {
			entry := map[string]any{
				"uri":                  site.URI,
				"refresh_interval_min": site.RefreshIntervalMin,
				"next_refresh_time":    site.NextRefreshISO,
			}
			if site.LastDispositionSet {
				entry["last_refresh_time"] = site.LastRefreshISO
				entry["last_refresh_status"] = site.LastDisposition
				if site.LastError != "" {
					entry["last_refresh_message"] = site.LastError
				}
			}
			sites = append(sites, entry)
		}
	}
	unl["validator_sites"] = sites
	return unl
}

func peerValidatorListProvider(source *validatorlist.Aggregator) func(string, uint32) (map[string]any, bool) {
	if source == nil {
		return nil
	}
	return source.AvailableList
}
