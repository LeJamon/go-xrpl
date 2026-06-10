package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const queueTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func queueTestAccountID(t *testing.T) [20]byte {
	t.Helper()
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(queueTestAccount)
	require.NoError(t, err)
	var id [20]byte
	copy(id[:], idBytes)
	return id
}

// TestAccountInfoQueueData_RealQueue exercises account_info's queue_data against
// a wired TxQ hook with two queued sequence txs (one a blocker) and verifies the
// per-tx and aggregate fields match rippled doAccountInfo (AccountInfo.cpp:193-283).
func TestAccountInfoQueueData_RealQueue(t *testing.T) {
	mock := newMockLedgerService()
	mock.accountInfo = &types.AccountInfo{
		Account:     queueTestAccount,
		Balance:     "100000000000",
		Sequence:    5,
		LedgerIndex: 3,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   false,
	}

	accountID := queueTestAccountID(t)
	services := &types.ServiceContainer{
		Ledger: mock,
		QueueAccountTxs: func(acc [20]byte) []types.QueuedTxInfo {
			require.Equal(t, accountID, acc)
			return []types.QueuedTxInfo{
				{
					SeqValue:      5,
					FeeLevel:      256,
					Fee:           10,
					MaxSpendDrops: 1000010,
					LastValid:     20,
					AuthChange:    false,
				},
				{
					SeqValue:      6,
					FeeLevel:      512,
					Fee:           15,
					MaxSpendDrops: 15,
					AuthChange:    true,
				},
			}
		},
	}

	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}
	method := &handlers.AccountInfoMethod{}
	paramsJSON, err := json.Marshal(map[string]any{
		"account":      queueTestAccount,
		"queue":        true,
		"ledger_index": "current",
	})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)

	resp := map[string]any{}
	raw, _ := json.Marshal(result)
	require.NoError(t, json.Unmarshal(raw, &resp))

	qd, ok := resp["queue_data"].(map[string]any)
	require.True(t, ok, "queue_data present")
	assert.EqualValues(t, 2, qd["txn_count"])
	assert.EqualValues(t, 2, qd["sequence_count"])
	assert.NotContains(t, qd, "ticket_count")
	assert.EqualValues(t, 5, qd["lowest_sequence"])
	assert.EqualValues(t, 6, qd["highest_sequence"])
	assert.Equal(t, true, qd["auth_change_queued"])
	// max_spend_drops_total = 1000010 + 15
	assert.Equal(t, "1000025", qd["max_spend_drops_total"])

	txs, ok := qd["transactions"].([]any)
	require.True(t, ok)
	require.Len(t, txs, 2)

	first := txs[0].(map[string]any)
	assert.EqualValues(t, 5, first["seq"])
	assert.Equal(t, "256", first["fee_level"])
	assert.Equal(t, "10", first["fee"])
	assert.Equal(t, "1000010", first["max_spend_drops"])
	assert.EqualValues(t, 20, first["LastLedgerSequence"])
	assert.Equal(t, false, first["auth_change"])

	second := txs[1].(map[string]any)
	assert.EqualValues(t, 6, second["seq"])
	assert.Equal(t, true, second["auth_change"])
	// No LastLedgerSequence when LastValid is 0.
	assert.NotContains(t, second, "LastLedgerSequence")
}

// TestAccountInfoQueueData_Tickets verifies ticket-keyed queued txs surface the
// ticket bounds/count instead of the sequence ones.
func TestAccountInfoQueueData_Tickets(t *testing.T) {
	mock := newMockLedgerService()
	mock.accountInfo = &types.AccountInfo{
		Account:     queueTestAccount,
		Balance:     "100000000000",
		Sequence:    5,
		LedgerIndex: 3,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
	}
	services := &types.ServiceContainer{
		Ledger: mock,
		QueueAccountTxs: func(acc [20]byte) []types.QueuedTxInfo {
			return []types.QueuedTxInfo{
				{SeqValue: 100, IsTicket: true, FeeLevel: 256, Fee: 10, MaxSpendDrops: 10},
			}
		},
	}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1, Services: services}
	method := &handlers.AccountInfoMethod{}
	paramsJSON, _ := json.Marshal(map[string]any{"account": queueTestAccount, "queue": true, "ledger_index": "current"})

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	resp := map[string]any{}
	raw, _ := json.Marshal(result)
	require.NoError(t, json.Unmarshal(raw, &resp))

	qd := resp["queue_data"].(map[string]any)
	assert.EqualValues(t, 1, qd["ticket_count"])
	assert.NotContains(t, qd, "sequence_count")
	assert.EqualValues(t, 100, qd["lowest_ticket"])
	assert.EqualValues(t, 100, qd["highest_ticket"])
	tx := qd["transactions"].([]any)[0].(map[string]any)
	assert.EqualValues(t, 100, tx["ticket"])
	assert.NotContains(t, tx, "seq")
}

// TestLedgerQueueData_RealQueue exercises the ledger method's top-level
// queue_data dump against a wired TxQ hook (rippled fillJsonQueue).
func TestLedgerQueueData_RealQueue(t *testing.T) {
	mock := &ledgerMock{mockLedgerService: newMockLedgerService()}
	reader := newDefaultLedgerReader(2, false)
	reader.closed = false
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	mock.currentLedgerIndex = 2

	accountID := queueTestAccountID(t)
	var txID [32]byte
	txID[0] = 0xAB
	services := &types.ServiceContainer{
		Ledger: mock,
		QueueAllTxs: func() []types.QueuedTxInfo {
			return []types.QueuedTxInfo{
				{
					Account:          accountID,
					TxID:             txID,
					SeqValue:         5,
					FeeLevel:         256,
					Fee:              10,
					MaxSpendDrops:    1000010,
					LastValid:        20,
					AuthChange:       true,
					RetriesRemaining: 10,
					PreflightResult:  "tesSUCCESS",
				},
			}
		},
	}

	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1, Services: services}
	method := &handlers.LedgerMethod{}
	paramsJSON, _ := json.Marshal(map[string]any{"ledger_index": "current", "queue": true})

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)

	resp := resultToMap(t, result)
	qd, ok := resp["queue_data"].([]any)
	require.True(t, ok, "queue_data present and non-empty")
	require.Len(t, qd, 1)
	entry := qd[0].(map[string]any)
	assert.Equal(t, "256", entry["fee_level"])
	assert.Equal(t, "10", entry["fee"])
	assert.Equal(t, "1000010", entry["max_spend_drops"])
	assert.Equal(t, true, entry["auth_change"])
	assert.Equal(t, queueTestAccount, entry["account"])
	assert.EqualValues(t, 10, entry["retries_remaining"])
	assert.Equal(t, "tesSUCCESS", entry["preflight_result"])
	assert.EqualValues(t, 20, entry["LastLedgerSequence"])
	// Never-retried tx omits last_result (rippled std::optional gate).
	assert.NotContains(t, entry, "last_result")
	// API v1 nests the tx body under "tx" with the hash.
	tx := entry["tx"].(map[string]any)
	assert.Equal(t, "AB00000000000000000000000000000000000000000000000000000000000000", tx["hash"])
}

// TestLedgerQueueData_EmptyOmitted verifies an empty TxQ yields no queue_data
// key at all (rippled only appends queue_data when the queue is non-empty).
func TestLedgerQueueData_EmptyOmitted(t *testing.T) {
	mock := &ledgerMock{mockLedgerService: newMockLedgerService()}
	reader := newDefaultLedgerReader(2, false)
	reader.closed = false
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	mock.currentLedgerIndex = 2
	services := &types.ServiceContainer{
		Ledger:      mock,
		QueueAllTxs: func() []types.QueuedTxInfo { return nil },
	}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1, Services: services}
	method := &handlers.LedgerMethod{}
	paramsJSON, _ := json.Marshal(map[string]any{"ledger_index": "current", "queue": true})

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	resp := resultToMap(t, result)
	assert.NotContains(t, resp, "queue_data")
}
