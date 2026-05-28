package handlers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchInfoMethod_EmptyWhenNotAcquiring(t *testing.T) {
	m := &handlers.FetchInfoMethod{}
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, IsAdmin: true}

	result, rpcErr := m.Handle(ctx, json.RawMessage(`{}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{}, resp["info"],
		"fetch_info returns an empty info object when not acquiring (rippled behavior)")
	_, hasClear := resp["clear"]
	assert.False(t, hasClear, "clear must be absent unless requested")
}

func TestFetchInfoMethod_PassesThroughSnapshot(t *testing.T) {
	snap := map[string]any{
		"42": map[string]any{
			"hash":        "ABCD",
			"have_header": true,
			"have_state":  false,
			"peers":       1,
		},
	}
	m := &handlers.FetchInfoMethod{}
	ctx := &types.RpcContext{
		Context: context.Background(),
		Role:    types.RoleAdmin,
		IsAdmin: true,
		Services: &types.ServiceContainer{
			FetchInfo: func() map[string]any { return snap },
		},
	}

	result, rpcErr := m.Handle(ctx, json.RawMessage(`{}`))
	require.Nil(t, rpcErr)

	resp := result.(map[string]any)
	assert.Equal(t, snap, resp["info"])
}

func TestFetchInfoMethod_ClearInvokesResetAndEchoes(t *testing.T) {
	cleared := false
	m := &handlers.FetchInfoMethod{}
	ctx := &types.RpcContext{
		Context: context.Background(),
		Role:    types.RoleAdmin,
		IsAdmin: true,
		Services: &types.ServiceContainer{
			FetchInfo:      func() map[string]any { return map[string]any{} },
			FetchInfoClear: func() { cleared = true },
		},
	}

	result, rpcErr := m.Handle(ctx, json.RawMessage(`{"clear":true}`))
	require.Nil(t, rpcErr)

	resp := result.(map[string]any)
	assert.True(t, cleared, "clear=true must invoke FetchInfoClear")
	assert.Equal(t, true, resp["clear"], "clear must be echoed back")
	assert.Equal(t, map[string]any{}, resp["info"])
}
