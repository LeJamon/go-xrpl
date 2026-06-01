package rpc

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctxWith builds a minimal RpcContext for conditionMet, wiring the mock as the
// ledger service.
func ctxWith(apiVersion int, mock *mockLedgerService) *types.RpcContext {
	return &types.RpcContext{
		ApiVersion: apiVersion,
		Services:   &types.ServiceContainer{Ledger: mock},
	}
}

func rippleNow() int64 { return time.Now().Unix() - protocol.RippleEpochUnix }

// syncedStandalone is a node that satisfies every condition: standalone, full,
// with a closed ledger.
func syncedStandalone() *mockLedgerService {
	m := newMockLedgerService()
	m.serverInfo = types.LedgerServerInfo{
		Standalone:      true,
		ServerState:     "full",
		ClosedLedgerSeq: 2,
	}
	return m
}

func TestConditionMet_NoConditionAlwaysAllowed(t *testing.T) {
	// A completely unsynced node still allows NoCondition methods.
	m := newMockLedgerService() // zero serverInfo: disconnected, no closed ledger
	assert.Nil(t, conditionMet(types.NoCondition, ctxWith(types.ApiVersion1, m)))
}

func TestConditionMet_AmendmentBlocked(t *testing.T) {
	m := syncedStandalone()
	m.amendmentBlocked = true
	rpcErr := conditionMet(types.NeedsCurrentLedger, ctxWith(types.ApiVersion1, m))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcAMENDMENT_BLOCKED, rpcErr.Code)
}

func TestConditionMet_NotSynced(t *testing.T) {
	m := newMockLedgerService()
	m.serverInfo = types.LedgerServerInfo{ServerState: "connected", ClosedLedgerSeq: 2}

	t.Run("apiVersion1 returns noNetwork", func(t *testing.T) {
		rpcErr := conditionMet(types.NeedsNetworkConnection, ctxWith(types.ApiVersion1, m))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNO_NETWORK, rpcErr.Code)
		assert.Equal(t, "noNetwork", rpcErr.ErrorString)
	})
	t.Run("apiVersion2 returns notSynced", func(t *testing.T) {
		rpcErr := conditionMet(types.NeedsNetworkConnection, ctxWith(types.ApiVersion2, m))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_SYNCED, rpcErr.Code)
	})
}

func TestConditionMet_NoClosedLedger(t *testing.T) {
	// Standalone + full but no closed ledger: skips the age/gap checks, still
	// refused by the closed-ledger check.
	m := newMockLedgerService()
	m.serverInfo = types.LedgerServerInfo{Standalone: true, ServerState: "full", ClosedLedgerSeq: 0}

	rpcErr := conditionMet(types.NeedsCurrentLedger, ctxWith(types.ApiVersion1, m))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNO_CLOSED, rpcErr.Code)
	assert.Equal(t, "noClosed", rpcErr.ErrorString)
}

func TestConditionMet_StandaloneSyncedPasses(t *testing.T) {
	assert.Nil(t, conditionMet(types.NeedsCurrentLedger, ctxWith(types.ApiVersion1, syncedStandalone())))
}

func TestConditionMet_NonStandaloneStaleValidated(t *testing.T) {
	// Networked node, full, with a closed ledger, but no validated ledger →
	// validated-ledger-age check fails with noCurrent.
	m := newMockLedgerService()
	m.serverInfo = types.LedgerServerInfo{
		ServerState:     "full",
		ClosedLedgerSeq: 100,
		HaveValidated:   false,
	}
	rpcErr := conditionMet(types.NeedsCurrentLedger, ctxWith(types.ApiVersion1, m))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNO_CURRENT, rpcErr.Code)
}

func TestConditionMet_NonStandaloneCurrentLagsValidated(t *testing.T) {
	m := newMockLedgerService()
	m.serverInfo = types.LedgerServerInfo{
		ServerState:              "full",
		OpenLedgerSeq:            100,
		ClosedLedgerSeq:          99,
		HaveValidated:            true,
		ValidatedLedgerSeq:       200, // current (100) + 10 < 200
		ValidatedLedgerCloseTime: rippleNow(),
	}
	rpcErr := conditionMet(types.NeedsCurrentLedger, ctxWith(types.ApiVersion1, m))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNO_CURRENT, rpcErr.Code)
}

func TestConditionMet_UNLBlocked(t *testing.T) {
	m := syncedStandalone()
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: m, UNLBlocked: func() bool { return true }},
	}
	rpcErr := conditionMet(types.NeedsCurrentLedger, ctx)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcEXPIRED_VALIDATOR_LIST, rpcErr.Code)
	assert.Equal(t, "unlBlocked", rpcErr.ErrorString)
}

func TestConditionMet_UNLNotBlockedPasses(t *testing.T) {
	m := syncedStandalone()
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: m, UNLBlocked: func() bool { return false }},
	}
	assert.Nil(t, conditionMet(types.NeedsCurrentLedger, ctx))
}

func TestConditionMet_AmendmentBlockedBeatsUNL(t *testing.T) {
	m := syncedStandalone()
	m.amendmentBlocked = true
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: m, UNLBlocked: func() bool { return true }},
	}
	rpcErr := conditionMet(types.NeedsCurrentLedger, ctx)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcAMENDMENT_BLOCKED, rpcErr.Code)
}

func TestConditionMet_NonStandaloneFreshPasses(t *testing.T) {
	m := newMockLedgerService()
	m.serverInfo = types.LedgerServerInfo{
		ServerState:              "full",
		OpenLedgerSeq:            200,
		ClosedLedgerSeq:          199,
		HaveValidated:            true,
		ValidatedLedgerSeq:       199,
		ValidatedLedgerCloseTime: rippleNow(),
	}
	assert.Nil(t, conditionMet(types.NeedsCurrentLedger, ctxWith(types.ApiVersion1, m)))
}
