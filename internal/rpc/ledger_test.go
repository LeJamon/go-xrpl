package rpc

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerReader implements types.LedgerReader for testing
type mockLedgerReader struct {
	seq                 uint32
	hash                [32]byte
	parentHash          [32]byte
	txMapHash           [32]byte
	stateMapHash        [32]byte
	closed              bool
	validated           bool
	totalDrops          uint64
	closeTime           int64
	closeTimeResolution uint32
	closeFlags          uint8
	parentCloseTime     int64
	transactions        []struct {
		hash [32]byte
		data []byte
	}
}

type snapshotStateLedgerReader struct {
	*mockLedgerReader
	entries map[[32]byte][]byte
	calls   int
}

func (l *snapshotStateLedgerReader) ForEachLedgerStateContext(ctx context.Context, fn func([32]byte, []byte) bool) error {
	l.calls++
	for key, data := range l.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !fn(key, data) {
			break
		}
	}
	return nil
}

func (m *mockLedgerReader) Sequence() uint32            { return m.seq }
func (m *mockLedgerReader) Hash() [32]byte              { return m.hash }
func (m *mockLedgerReader) ParentHash() [32]byte        { return m.parentHash }
func (m *mockLedgerReader) IsClosed() bool              { return m.closed }
func (m *mockLedgerReader) IsValidated() bool           { return m.validated }
func (m *mockLedgerReader) TotalDrops() uint64          { return m.totalDrops }
func (m *mockLedgerReader) CloseTime() int64            { return m.closeTime }
func (m *mockLedgerReader) CloseTimeResolution() uint32 { return m.closeTimeResolution }
func (m *mockLedgerReader) CloseFlags() uint8           { return m.closeFlags }
func (m *mockLedgerReader) ParentCloseTime() int64      { return m.parentCloseTime }
func (m *mockLedgerReader) TxMapHash() [32]byte         { return m.txMapHash }
func (m *mockLedgerReader) StateMapHash() [32]byte      { return m.stateMapHash }
func (m *mockLedgerReader) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	for _, tx := range m.transactions {
		if !fn(tx.hash, tx.data) {
			break
		}
	}
	return nil
}

// ledgerMock wraps mockLedgerService and overrides GetLedgerBySequence/GetLedgerByHash
type ledgerMock struct {
	*mockLedgerService
	getLedgerBySequenceFn func(seq uint32) (types.LedgerReader, error)
	getLedgerByHashFn     func(hash [32]byte) (types.LedgerReader, error)
	getLedgerDataFn       func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error)
}

func (m *ledgerMock) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if m.getLedgerBySequenceFn != nil {
		return m.getLedgerBySequenceFn(seq)
	}
	return m.mockLedgerService.GetLedgerBySequence(seq)
}

func (m *ledgerMock) GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	if m.getLedgerDataFn != nil {
		return m.getLedgerDataFn(ledgerIndex, limit, marker)
	}
	return m.mockLedgerService.GetLedgerData(ctx, ledgerIndex, limit, marker)
}

func (m *ledgerMock) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	if m.getLedgerByHashFn != nil {
		return m.getLedgerByHashFn(hash)
	}
	return m.mockLedgerService.GetLedgerByHash(hash)
}

func (m *ledgerMock) GetLedgerByHashContext(ctx context.Context, hash [32]byte) (types.LedgerReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.GetLedgerByHash(hash)
}

// newDefaultLedgerReader creates a default mockLedgerReader with typical values
func newDefaultLedgerReader(seq uint32, validated bool) *mockLedgerReader {
	var hash [32]byte
	hash[0] = byte(seq)
	hash[31] = 0xAA

	var parentHash [32]byte
	if seq > 1 {
		parentHash[0] = byte(seq - 1)
		parentHash[31] = 0xAA
	}

	return &mockLedgerReader{
		seq:                 seq,
		hash:                hash,
		parentHash:          parentHash,
		closed:              validated || seq < 3,
		validated:           validated,
		totalDrops:          99999999999999980,
		closeTime:           776000030,
		closeTimeResolution: 10,
		closeFlags:          0,
		parentCloseTime:     776000020,
	}
}

func expectedLedgerHeaderHex(l *mockLedgerReader) string {
	data := make([]byte, 0, 118)
	data = binary.BigEndian.AppendUint32(data, l.Sequence())
	data = binary.BigEndian.AppendUint64(data, l.TotalDrops())
	parentHash := l.ParentHash()
	txHash := l.TxMapHash()
	stateHash := l.StateMapHash()
	data = append(data, parentHash[:]...)
	data = append(data, txHash[:]...)
	data = append(data, stateHash[:]...)
	data = binary.BigEndian.AppendUint32(data, uint32(l.ParentCloseTime()))
	data = binary.BigEndian.AppendUint32(data, uint32(l.CloseTime()))
	data = append(data, byte(l.CloseTimeResolution()), l.CloseFlags())
	return strings.ToUpper(hex.EncodeToString(data))
}

// TestLedgerBasicRequest tests basic ledger request with default params
// Based on rippled LedgerRPC_test.cpp testLedgerRequest()
func TestLedgerBasicRequest(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	currentReader := newDefaultLedgerReader(3, false)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		switch seq {
		case 2:
			return reader, nil
		case 3:
			return currentReader, nil
		}
		return nil, svcerr.ErrLedgerNotFound
	}
	mock.getLedgerDataFn = func(string, uint32, string) (*types.LedgerDataResult, error) {
		return &types.LedgerDataResult{}, nil
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Default params returns closed and open summaries", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		closed := resp["closed"].(map[string]any)
		open := resp["open"].(map[string]any)
		assert.Equal(t, true, closed["closed"])
		assert.Equal(t, "2", closed["ledger_index"])
		assert.Contains(t, closed, "ledger_hash")
		assert.Equal(t, map[string]any{
			"parent_hash":  handlers.FormatLedgerHash(currentReader.ParentHash()),
			"ledger_index": "3",
			"closed":       false,
		}, open)
	})

	t.Run("Default params ignore dump flags", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"full":true,"accounts":true,"transactions":true}`))
		require.Nil(t, rpcErr)
		resp := resultToMap(t, result)
		assert.Contains(t, resp, "closed")
		assert.Contains(t, resp, "open")
		assert.NotContains(t, resp, "ledger")
	})

	for _, params := range []string{
		`{"ledger_index":""}`,
		`{"ledger":""}`,
	} {
		t.Run("Empty index selects current "+params, func(t *testing.T) {
			result, rpcErr := method.Handle(ctx, json.RawMessage(params))
			require.Nil(t, rpcErr)
			resp := resultToMap(t, result)
			assert.NotContains(t, resp, "closed")
			assert.NotContains(t, resp, "open")
			assert.Equal(t, float64(3), resp["ledger_current_index"])
		})
	}

	for _, params := range []string{
		`{"ledger_index":null}`,
		`{"ledger_hash":""}`,
		`{"ledger_hash":null}`,
		`{"ledger":null}`,
	} {
		t.Run("Invalid present selector "+params, func(t *testing.T) {
			result, rpcErr := method.Handle(ctx, json.RawMessage(params))
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		})
	}

	t.Run("Empty index does not bypass dump permission", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"","full":true}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNO_PERMISSION, rpcErr.Code)
	})

	t.Run("Malformed selector precedes dump permission", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_hash":"DEADBEEF","full":true}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Queue requires open ledger", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"validated","queue":true}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)

		_, rpcErr = method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","queue":true}`))
		require.Nil(t, rpcErr)
	})

	t.Run("Selected current response shape", func(t *testing.T) {
		for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
			ctx.ApiVersion = apiVersion
			result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"current"}`))
			require.Nil(t, rpcErr)
			ledgerIndex := any("3")
			if apiVersion > 1 {
				ledgerIndex = float64(3)
			}
			assert.Equal(t, map[string]any{
				"ledger": map[string]any{
					"parent_hash":  handlers.FormatLedgerHash(currentReader.ParentHash()),
					"ledger_index": ledgerIndex,
					"closed":       false,
				},
				"ledger_current_index": float64(3),
				"validated":            false,
			}, resultToMap(t, result))
		}
		ctx.ApiVersion = types.ApiVersion1
	})

	t.Run("Selected binary response shape", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","binary":true}`))
		require.Nil(t, rpcErr)
		assert.Equal(t, map[string]any{
			"ledger":               map[string]any{"closed": false},
			"ledger_current_index": float64(3),
			"validated":            false,
		}, resultToMap(t, result))

		result, rpcErr = method.Handle(ctx, json.RawMessage(`{"ledger_index":"validated","binary":true}`))
		require.Nil(t, rpcErr)
		assert.Equal(t, map[string]any{
			"ledger": map[string]any{
				"closed":      true,
				"ledger_data": expectedLedgerHeaderHex(reader),
			},
			"ledger_hash":  handlers.FormatLedgerHash(reader.Hash()),
			"ledger_index": float64(2),
			"validated":    true,
		}, resultToMap(t, result))
	})

	t.Run("Open full response omits closed", func(t *testing.T) {
		ctx.Role = types.RoleAdmin
		ctx.Unlimited = true
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","full":true}`))
		require.Nil(t, rpcErr)
		closeTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(currentReader.CloseTime()) * time.Second)
		assert.Equal(t, map[string]any{
			"ledger": map[string]any{
				"accountState":          []any{},
				"account_hash":          handlers.FormatLedgerHash(currentReader.StateMapHash()),
				"close_flags":           float64(currentReader.CloseFlags()),
				"close_time":            float64(currentReader.CloseTime()),
				"close_time_human":      closeTime.Format("2006-Jan-02 15:04:05.000000000 UTC"),
				"close_time_iso":        closeTime.Format(time.RFC3339),
				"close_time_resolution": float64(currentReader.CloseTimeResolution()),
				"ledger_hash":           handlers.FormatLedgerHash(currentReader.Hash()),
				"ledger_index":          "3",
				"parent_close_time":     float64(currentReader.ParentCloseTime()),
				"parent_hash":           handlers.FormatLedgerHash(currentReader.ParentHash()),
				"total_coins":           "99999999999999980",
				"transaction_hash":      handlers.FormatLedgerHash(currentReader.TxMapHash()),
				"transactions":          []any{},
			},
			"ledger_current_index": float64(3),
			"validated":            false,
		}, resultToMap(t, result))
		ctx.Role = types.RoleGuest
		ctx.Unlimited = false
	})

	t.Run("Deprecated type field emits warning", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"type":"ledger"}`))
		require.Nil(t, rpcErr)
		warnings := resultToMap(t, result)["warnings"].([]any)
		require.Len(t, warnings, 1)
		warning, ok := warnings[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(2004), warning["id"])
	})

	t.Run("Numeric ledger_index", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": 2,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]any)
		assert.Equal(t, true, ledger["closed"])
		assert.Equal(t, "2", ledger["ledger_index"])
	})

	t.Run("String numeric ledger_index", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "2",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]any)
		assert.Equal(t, true, ledger["closed"])
		assert.Equal(t, "2", ledger["ledger_index"])
	})
}

// TestLedgerBadInput tests bad input handling for ledger method
// Based on rippled LedgerRPC_test.cpp testBadInput()
func TestLedgerBadInput(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq <= 2 {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name        string
		params      any
		expectError bool
	}{
		{
			name:        "Invalid string ledger_index (potato)",
			params:      map[string]any{"ledger_index": "potato"},
			expectError: true,
		},
		{
			name:        "Non-existent ledger_index",
			params:      map[string]any{"ledger_index": 10},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				assert.Nil(t, result)
				require.NotNil(t, rpcErr, "Expected RPC error")
			}
		})
	}
}

// TestLedgerCurrentRequest tests ledger_index "current" requests
// Based on rippled LedgerRPC_test.cpp testLedgerCurrent() and testLedgerRequest()
func TestLedgerCurrentRequest(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	currentReader := newDefaultLedgerReader(3, false)
	currentReader.closed = false
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 3 {
			return currentReader, nil
		}
		return nil, errors.New("not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params := map[string]any{
		"ledger_index": "current",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
	require.NotNil(t, result)

	resp := resultToMap(t, result)
	ledger := resp["ledger"].(map[string]any)
	assert.Equal(t, false, ledger["closed"])
	assert.Equal(t, "3", ledger["ledger_index"])
	// Current ledger should not be validated
	assert.Equal(t, false, resp["validated"])
	assert.Equal(t, float64(3), resp["ledger_current_index"])
	assert.NotContains(t, resp, "ledger_hash")
	assert.NotContains(t, resp, "ledger_index")
}

// TestLedgerFullOption tests the full option with transactions and expand
// Based on rippled LedgerRPC_test.cpp testLedgerFull()
func TestLedgerFullOption(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	// Add some mock transactions
	var txHash1, txHash2 [32]byte
	txHash1[0] = 0x01
	txHash2[0] = 0x02
	reader.transactions = []struct {
		hash [32]byte
		data []byte
	}{
		{hash: txHash1, data: []byte{0x01, 0x02, 0x03}},
		{hash: txHash2, data: []byte{0x04, 0x05, 0x06}},
	}

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Transactions true returns tx hashes", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": 2,
			"transactions": true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]any)
		assert.Contains(t, ledger, "transactions")
		txs := ledger["transactions"].([]any)
		assert.Equal(t, 2, len(txs))
		// Without expand, should be hash strings
		_, isString := txs[0].(string)
		assert.True(t, isString, "Without expand, transactions should be hash strings")
	})

	t.Run("Transactions true with expand returns objects", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": 2,
			"transactions": true,
			"expand":       true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]any)
		assert.Contains(t, ledger, "transactions")
		txs := ledger["transactions"].([]any)
		assert.Equal(t, 2, len(txs))
		// With expand, should be objects with hash field
		txObj, isMap := txs[0].(map[string]any)
		assert.True(t, isMap, "With expand, transactions should be objects")
		assert.Contains(t, txObj, "hash")
	})
}

func TestLedgerExpandedDeliveredAmountHistoricalCloseTime(t *testing.T) {
	tests := []struct {
		name              string
		closeTime         int64
		expectedDelivered string
	}{
		{
			name:              "at close-time cutoff",
			closeTime:         446_000_000,
			expectedDelivered: "unavailable",
		},
		{
			name:              "after close-time cutoff",
			closeTime:         446_000_001,
			expectedDelivered: "100",
		},
	}

	for _, tc := range tests {
		for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
			apiName := "api_v1"
			metaKey := "metaData"
			if apiVersion == types.ApiVersion2 {
				apiName = "api_v2"
				metaKey = "meta"
			}
			t.Run(tc.name+"/"+apiName, func(t *testing.T) {
				txData, err := json.Marshal(handlers.StoredTransaction{
					TxJSON: map[string]any{
						"TransactionType": "Payment",
						"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
						"Destination":     "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
						"Amount":          "100",
						"Fee":             "10",
						"Sequence":        1,
						"SigningPubKey":   "",
					},
					Meta: map[string]any{"TransactionResult": "tesSUCCESS"},
				})
				require.NoError(t, err)

				reader := newDefaultLedgerReader(4_594_094, true)
				reader.closeTime = tc.closeTime
				reader.transactions = []struct {
					hash [32]byte
					data []byte
				}{
					{hash: [32]byte{1}, data: txData},
				}
				mock := &ledgerMock{mockLedgerService: newMockLedgerService()}
				mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
					require.Equal(t, uint32(4_594_094), seq)
					return reader, nil
				}
				ctx := &types.RpcContext{
					Context:    context.Background(),
					Role:       types.RoleGuest,
					ApiVersion: apiVersion,
					Services:   &types.ServiceContainer{Ledger: mock},
				}
				params := json.RawMessage(`{"ledger_index":4594094,"transactions":true,"expand":true}`)

				result, rpcErr := (&handlers.LedgerMethod{}).Handle(ctx, params)
				require.Nil(t, rpcErr)
				response := result.(map[string]any)
				ledger := response["ledger"].(map[string]any)
				entry := ledger["transactions"].([]any)[0].(map[string]any)
				meta := entry[metaKey].(map[string]any)
				require.Equal(t, tc.expectedDelivered, meta["delivered_amount"])
			})
		}
	}
}

// TestLedgerAccountsOption tests the accounts option
// Based on rippled LedgerRPC_test.cpp testLedgerAccounts()
func TestLedgerAccountsOption(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	// One account-state node to dump on the permitted path.
	stateIndex := "00000000000000000000000000000000000000000000000000000000DEADBEEF"
	accountRootBlob, decErr := hex.DecodeString(
		"1100612200000000240000000125000000016240000000000F424081140000000000000000000000000000000000000001")
	require.NoError(t, decErr)
	var dataSelector string
	var dumpLimit uint32
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		dataSelector = ledgerIndex
		dumpLimit = limit
		return &types.LedgerDataResult{
			LedgerIndex: 2,
			State: []types.LedgerDataItem{
				{Index: stateIndex, Data: accountRootBlob},
			},
		}, nil
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}

	// full implies expand + transactions + accounts, so the state dump is
	// expanded SLE JSON (LedgerToJson.cpp isFull/isExpanded).
	params := map[string]any{
		"ledger_index": 2,
		"full":         true,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	// A non-unlimited (guest) role is denied: rippled gates accounts/full
	// behind isUnlimited else rpcNO_PERMISSION (LedgerHandler.cpp:66-72).
	guestCtx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}
	_, rpcErr := method.Handle(guestCtx, paramsJSON)
	require.NotNil(t, rpcErr, "guest must be denied the full/accounts dump")
	assert.Equal(t, types.RpcNO_PERMISSION, rpcErr.Code)

	// An unlimited (admin) role is permitted and dumps the state into the
	// ledger object's accountState array.
	adminCtx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Unlimited:  true,
		Services:   services,
	}
	result, rpcErr := method.Handle(adminCtx, paramsJSON)
	require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
	require.NotNil(t, result)

	resp := resultToMap(t, result)
	ledgerObj, ok := resp["ledger"].(map[string]any)
	require.True(t, ok, "ledger object present")
	state, ok := ledgerObj["accountState"].([]any)
	require.True(t, ok, "accountState array present")
	require.Len(t, state, 1)
	entry := state[0].(map[string]any)
	assert.Equal(t, stateIndex, entry["index"])
	assert.Equal(t, "AccountRoot", entry["LedgerEntryType"])
	wantSelector := hex.EncodeToString(reader.hash[:])
	assert.Equal(t, wantSelector, dataSelector)
	assert.Equal(t, uint32(256), dumpLimit, "full state walks must use a positive page size")

	openBase := newDefaultLedgerReader(3, false)
	openBase.closed = false
	openReader := &snapshotStateLedgerReader{
		mockLedgerReader: openBase,
		entries: map[[32]byte][]byte{
			{0x42}: accountRootBlob,
		},
	}
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		switch seq {
		case 2:
			return reader, nil
		case 3:
			return openReader, nil
		default:
			return nil, svcerr.ErrLedgerNotFound
		}
	}
	dataSelector = ""
	currentParams, err := json.Marshal(map[string]any{
		"ledger_index": "current",
		"accounts":     true,
		"expand":       true,
	})
	require.NoError(t, err)
	result, rpcErr = method.Handle(adminCtx, currentParams)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)
	assert.Empty(t, dataSelector)
	assert.Equal(t, 1, openReader.calls)

	openReader.entries = map[[32]byte][]byte{{0x43}: {1, 2, 3}}
	result, rpcErr = method.Handle(adminCtx, currentParams)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

func TestLedgerQueueRequiresOpenSelector(t *testing.T) {
	mock := &ledgerMock{mockLedgerService: newMockLedgerService()}
	closed := newDefaultLedgerReader(2, true)
	open := newDefaultLedgerReader(3, false)
	mock.getLedgerBySequenceFn = func(sequence uint32) (types.LedgerReader, error) {
		switch sequence {
		case 2:
			return closed, nil
		case 3:
			return open, nil
		default:
			return nil, errors.New("not found")
		}
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: mock},
	}
	method := &handlers.LedgerMethod{}

	result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"validated","queue":true}`))
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Invalid parameters.", rpcErr.Message)

	result, rpcErr = method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","queue":true}`))
	require.Nil(t, rpcErr)
	require.NotNil(t, result)
}

// TestLedgerLookupByHash tests ledger lookup by hash
// Based on rippled LedgerRPC_test.cpp testLookupLedger() hash section
func TestLedgerLookupByHash(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	var expectedHash [32]byte
	expectedHash[0] = 0x4B
	expectedHash[1] = 0xC5
	reader.hash = expectedHash

	mock.getLedgerByHashFn = func(hash [32]byte) (types.LedgerReader, error) {
		if hash == expectedHash {
			return reader, nil
		}
		return nil, svcerr.ErrLedgerNotFound
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	hashStr := hex.EncodeToString(expectedHash[:])

	t.Run("Valid hash lookup", func(t *testing.T) {
		params := map[string]any{
			"ledger_hash": hashStr,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		assert.Contains(t, resp, "ledger")
		assert.Contains(t, resp, "ledger_hash")
	})

	t.Run("Invalid hash - too long", func(t *testing.T) {
		params := map[string]any{
			"ledger_hash": "DEADBEEF" + hashStr,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Invalid hash - non-hex characters", func(t *testing.T) {
		params := map[string]any{
			"ledger_hash": "2E81FC6EC0DD943197EGC7E3FBE9AE307F2775F2F7485BB37307984C3C0F2340",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Valid hash format but not found", func(t *testing.T) {
		params := map[string]any{
			"ledger_hash": "8C3EEDB3124D92E49E75D81A8826A2E65A75FD71FC3FD6F36FEB803C5F1D812D",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("Storage failure", func(t *testing.T) {
		mock.getLedgerByHashFn = func([32]byte) (types.LedgerReader, error) {
			return nil, errors.New("storage unavailable")
		}
		paramsJSON, err := json.Marshal(map[string]any{"ledger_hash": hashStr})
		require.NoError(t, err)
		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	})
}

// TestLedgerResponseStructure tests that the response contains all expected fields
// Based on rippled LedgerRPC_test.cpp testLookupLedger() verifying response shape
func TestLedgerResponseStructure(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params := map[string]any{
		"ledger_index": "validated",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	closeTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(reader.CloseTime()) * time.Second)
	assert.Equal(t, map[string]any{
		"ledger": map[string]any{
			"account_hash":          handlers.FormatLedgerHash(reader.StateMapHash()),
			"close_flags":           float64(reader.CloseFlags()),
			"close_time":            float64(reader.CloseTime()),
			"close_time_human":      closeTime.Format("2006-Jan-02 15:04:05.000000000 UTC"),
			"close_time_iso":        closeTime.Format(time.RFC3339),
			"close_time_resolution": float64(reader.CloseTimeResolution()),
			"closed":                true,
			"ledger_hash":           handlers.FormatLedgerHash(reader.Hash()),
			"ledger_index":          "2",
			"parent_close_time":     float64(reader.ParentCloseTime()),
			"parent_hash":           handlers.FormatLedgerHash(reader.ParentHash()),
			"total_coins":           "99999999999999980",
			"transaction_hash":      handlers.FormatLedgerHash(reader.TxMapHash()),
		},
		"ledger_hash":  handlers.FormatLedgerHash(reader.Hash()),
		"ledger_index": float64(2),
		"validated":    true,
	}, resultToMap(t, result))
}

// TestLedgerServiceUnavailable tests behavior when ledger service is not available
func TestLedgerServiceUnavailable(t *testing.T) {
	method := &handlers.LedgerMethod{}

	t.Run("Nil services", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   nil,
		}

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Equal(t, "Internal error.", rpcErr.Message)
	})

	t.Run("Nil ledger in services", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: nil},
		}

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Equal(t, "Internal error.", rpcErr.Message)
	})
}

// TestLedgerNilLedgerReturned tests behavior when GetLedgerBySequence returns nil
func TestLedgerNilLedgerReturned(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		return nil, errors.New("not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr, "Expected error when no ledger is found")
}

// TestLedgerMethodMetadata tests the method's metadata
func TestLedgerMethodMetadata(t *testing.T) {
	method := &handlers.LedgerMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"ledger should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestLedgerLookupByIndex tests ledger lookup by various ledger_index values
// Based on rippled LedgerRPC_test.cpp testLookupLedger() ledger_index section
func TestLedgerLookupByIndex(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}

	readers := map[uint32]*mockLedgerReader{
		1: newDefaultLedgerReader(1, true),
		2: newDefaultLedgerReader(2, true),
		3: newDefaultLedgerReader(3, false),
	}
	readers[3].closed = false

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if r, ok := readers[seq]; ok {
			return r, nil
		}
		return nil, errors.New("not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("closed keyword", func(t *testing.T) {
		params := map[string]any{"ledger_index": "closed"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		assert.Contains(t, resp, "ledger")
		assert.Contains(t, resp, "ledger_hash")
	})

	t.Run("validated keyword", func(t *testing.T) {
		params := map[string]any{"ledger_index": "validated"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		assert.Contains(t, resp, "ledger")
		assert.Contains(t, resp, "ledger_hash")
	})

	t.Run("current keyword", func(t *testing.T) {
		params := map[string]any{"ledger_index": "current"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]any)
		assert.Equal(t, "3", ledger["ledger_index"])
	})

	t.Run("invalid keyword", func(t *testing.T) {
		params := map[string]any{"ledger_index": "invalid"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Numeric index 1", func(t *testing.T) {
		params := map[string]any{"ledger_index": 1}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]any)
		assert.Equal(t, "1", ledger["ledger_index"])
	})

	t.Run("Numeric index out of range", func(t *testing.T) {
		params := map[string]any{"ledger_index": 7}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code, "Should return lgrNotFound error")
	})
}

// resultToMap is a test helper that converts a handler result to map[string]interface{}
func resultToMap(t *testing.T, result any) map[string]any {
	t.Helper()
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)
	return resp
}
