package rpc

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func feeRequest(t *testing.T, services *types.ServiceContainer) map[string]interface{} {
	t.Helper()
	method := &handlers.FeeMethod{}
	result, rpcErr := method.Handle(&types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}, nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)
	resp, ok := result.(map[string]interface{})
	require.True(t, ok, "fee response is not a map")
	return resp
}

// TestFee_FallsBackToIdleDefaults pins the no-TxQ-hook path: the
// handler reproduces rippled's idle-state response (queue empty,
// reference_level=256, all drops fields equal to base_fee).
func TestFee_FallsBackToIdleDefaults(t *testing.T) {
	mock := newMockLedgerService()
	mock.standalone = false
	mock.currentLedgerIndex = 7
	services := &types.ServiceContainer{Ledger: mock}

	resp := feeRequest(t, services)

	require.Equal(t, uint32(7), resp["ledger_current_index"])
	require.Equal(t, "0", resp["current_ledger_size"])
	require.Equal(t, "0", resp["current_queue_size"])
	require.Equal(t, "32", resp["expected_ledger_size"])
	require.Equal(t, "640", resp["max_queue_size"])

	levels := resp["levels"].(map[string]interface{})
	require.Equal(t, "256", levels["reference_level"])
	require.Equal(t, "256", levels["minimum_level"])
	require.Equal(t, "256", levels["median_level"])
	require.Equal(t, "256", levels["open_ledger_level"])

	drops := resp["drops"].(map[string]interface{})
	require.Equal(t, "10", drops["base_fee"])
	require.Equal(t, "10", drops["median_fee"])
	require.Equal(t, "10", drops["minimum_fee"])
	require.Equal(t, "10", drops["open_ledger_fee"])
}

// TestFee_StandaloneFallback bumps the idle expected_ledger_size to
// 1000 and max_queue_size to 20_000 (rippled minimumTxnInLedgerSA=1000).
func TestFee_StandaloneFallback(t *testing.T) {
	mock := newMockLedgerService()
	mock.standalone = true
	services := &types.ServiceContainer{Ledger: mock}

	resp := feeRequest(t, services)
	require.Equal(t, "1000", resp["expected_ledger_size"])
	require.Equal(t, "20000", resp["max_queue_size"])
}

// TestFee_LiveMetrics_Escalating verifies the per-level drops follow
// rippled's TxQ.cpp:1898-1907 algebra: median/min/open all scaled by
// (level * baseFee) / 256, with the open-ledger value rounded up to
// preserve the snapshot's level on round-trip.
func TestFee_LiveMetrics_Escalating(t *testing.T) {
	mock := newMockLedgerService()
	mock.standalone = false
	mock.currentLedgerIndex = 99
	services := &types.ServiceContainer{Ledger: mock}
	maxQ := uint32(640)
	services.TxQFeeMetrics = func() types.TxQFeeMetrics {
		return types.TxQFeeMetrics{
			TxCount:               5,
			TxQMaxSize:            &maxQ,
			TxInLedger:            42,
			TxPerLedger:           32,
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 256,
			MedFeeLevel:           512,
			OpenLedgerFeeLevel:    1024,
		}
	}

	resp := feeRequest(t, services)
	require.Equal(t, "5", resp["current_queue_size"])
	require.Equal(t, "42", resp["current_ledger_size"])
	require.Equal(t, "32", resp["expected_ledger_size"])
	require.Equal(t, "640", resp["max_queue_size"])

	levels := resp["levels"].(map[string]interface{})
	require.Equal(t, "256", levels["reference_level"])
	require.Equal(t, "512", levels["median_level"])
	require.Equal(t, "1024", levels["open_ledger_level"])

	drops := resp["drops"].(map[string]interface{})
	require.Equal(t, "10", drops["base_fee"])
	require.Equal(t, "20", drops["median_fee"])
	require.Equal(t, "10", drops["minimum_fee"])
	require.Equal(t, "40", drops["open_ledger_fee"])
}

// TestFee_QueueFull_SwapsMinimumBase mirrors rippled TxQ.cpp:1900-1902:
// when txCount >= txQMaxSize, the minimum_fee uses the effective base
// (clamped to 1 if base==0 and escalation is active).
func TestFee_QueueFull_SwapsMinimumBase(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	mock.standalone = false
	mock.currentLedgerIndex = 99
	mock.baseFee = 0
	services := &types.ServiceContainer{Ledger: mock}
	maxQ := uint32(2)
	services.TxQFeeMetrics = func() types.TxQFeeMetrics {
		return types.TxQFeeMetrics{
			TxCount:               2,
			TxQMaxSize:            &maxQ,
			TxInLedger:            10,
			TxPerLedger:           32,
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 768,
			MedFeeLevel:           256,
			OpenLedgerFeeLevel:    512,
		}
	}

	resp := feeRequest(t, services)
	drops := resp["drops"].(map[string]interface{})

	require.Equal(t, "0", drops["base_fee"])
	require.Equal(t, "3", drops["minimum_fee"])
	require.Equal(t, "2", drops["open_ledger_fee"])
}

// TestFee_NilTxQMaxSize_OmitsMaxQueueSize matches rippled
// TxQ.cpp:1879-1880: max_queue_size is only emitted when the queue
// has a configured upper bound.
func TestFee_NilTxQMaxSize_OmitsMaxQueueSize(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}
	services.TxQFeeMetrics = func() types.TxQFeeMetrics {
		return types.TxQFeeMetrics{
			TxCount:               0,
			TxQMaxSize:            nil,
			TxPerLedger:           32,
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 256,
			MedFeeLevel:           256,
			OpenLedgerFeeLevel:    256,
		}
	}

	resp := feeRequest(t, services)
	_, hasMaxQueue := resp["max_queue_size"]
	require.False(t, hasMaxQueue, "max_queue_size must be omitted when TxQ has no configured limit")
}
