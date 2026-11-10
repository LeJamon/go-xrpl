package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledgerRequestMock overrides the by-hash / by-sequence lookups so a ledger can
// be reported as locally present, while inheriting the rest of mockLedgerService.
type ledgerRequestMock struct {
	*mockLedgerService
	byHash map[[32]byte]types.LedgerReader
	bySeq  map[uint32]types.LedgerReader
}

func (m *ledgerRequestMock) GetLedgerByHash(h [32]byte) (types.LedgerReader, error) {
	if l, ok := m.byHash[h]; ok {
		return l, nil
	}
	return nil, svcerr.ErrLedgerNotFound
}

func (m *ledgerRequestMock) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if l, ok := m.bySeq[seq]; ok {
		return l, nil
	}
	return nil, svcerr.ErrLedgerNotFound
}

func TestLedgerRequest_ServesLocalLedgerByHash(t *testing.T) {
	var hash [32]byte
	hash[0], hash[31] = 0xAB, 0xCD
	lr := &mockLedgerReader{seq: 5, hash: hash, closed: true, validated: true, totalDrops: 99000000, closeTime: 776000030}

	mock := &ledgerRequestMock{
		mockLedgerService: newMockLedgerService(),
		byHash:            map[[32]byte]types.LedgerReader{hash: lr},
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock}),
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_hash":"`+hex.EncodeToString(hash[:])+`"}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, uint32(5), resp["ledger_index"])
	ledger, ok := resp["ledger"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, strings.ToUpper(hex.EncodeToString(hash[:])), ledger["ledger_hash"])
	assert.Equal(t, "5", ledger["ledger_index"])
	assert.Equal(t, "2024-Aug-03 11:33:50.000000000 UTC", ledger["close_time_human"])
	assert.Equal(t, "2024-08-03T11:33:50Z", ledger["close_time_iso"])
}

func TestLedgerRequest_ServesLocalLedgerByIndex(t *testing.T) {
	var hash [32]byte
	hash[0] = 0x11
	lr := &mockLedgerReader{seq: 1, hash: hash, closed: true, validated: true}

	mock := &ledgerRequestMock{
		mockLedgerService: newMockLedgerService(), // validated ledger is seq 2
		bySeq:             map[uint32]types.LedgerReader{1: lr},
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock}),
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_index": 1}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, uint32(1), resp["ledger_index"])
	ledger, ok := resp["ledger"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, ledger, "close_time_human")
	assert.NotContains(t, ledger, "close_time_iso")
}

// TestLedgerRequest_AcquiringTargetReturnsBareSnapshot covers rippled's common
// not-local path: when the target hash is known and its ledger is being
// fetched, rippled returns the bare acquisition snapshot as the result, with no
// error wrapper (RPCHelpers.cpp:1137-1138).
func TestLedgerRequest_AcquiringTargetReturnsBareSnapshot(t *testing.T) {
	var hash [32]byte
	hash[0] = 0x42
	hexHash := hex.EncodeToString(hash[:])

	acquiring := map[string]any{
		"hash":        strings.ToUpper(hexHash),
		"have_header": false,
		"peers":       1,
		"timeouts":    0,
	}
	var gotHash [32]byte
	mock := &ledgerRequestMock{mockLedgerService: newMockLedgerService()}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			Ledger: mock,
			RequestLedger: func(h [32]byte, seq uint32) (map[string]any, bool, bool) {
				gotHash = h
				return acquiring, true, false
			},
		}),
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_hash":"`+hexHash+`"}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, acquiring, resp, "target acquisition returns the bare snapshot")
	assert.Nil(t, resp["error"], "the bare snapshot must not carry an error field")
	assert.Equal(t, hash, gotHash, "the requested hash must be forwarded to the acquisition coordinator")
}

// TestLedgerRequest_AcquiringReferenceReturnsLgrNotFound covers rippled's
// deep-index path: when a reference ledger must be fetched to learn the
// target's hash, rippled wraps the snapshot as lgrNotFound + acquiring
// (RPCHelpers.cpp:1096-1110).
func TestLedgerRequest_AcquiringReferenceReturnsLgrNotFound(t *testing.T) {
	var hash [32]byte
	hash[0] = 0x42
	hexHash := hex.EncodeToString(hash[:])

	acquiring := map[string]any{
		"hash":        strings.ToUpper(hexHash),
		"have_header": false,
		"peers":       1,
		"timeouts":    0,
	}
	mock := &ledgerRequestMock{mockLedgerService: newMockLedgerService()}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			Ledger: mock,
			RequestLedger: func(h [32]byte, seq uint32) (map[string]any, bool, bool) {
				return acquiring, true, true
			},
		}),
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_hash":"`+hexHash+`"}`))
	// rippled returns this as a result body (error + acquiring members), not a
	// transport-level error.
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "lgrNotFound", resp["error"])
	assert.Equal(t, acquiring, resp["acquiring"])
}

func TestLedgerRequest_NotFoundWithoutSubsystem(t *testing.T) {
	var hash [32]byte
	hash[0] = 0x77
	mock := &ledgerRequestMock{mockLedgerService: newMockLedgerService()}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock}), // RequestLedger nil
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_hash":"`+hex.EncodeToString(hash[:])+`"}`))
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcLGR_NOT_FOUND, rpcErr.Code)
}

// rippled getOrAcquireLedger (RPCLedgerHelpers.cpp:396-440): selector
// presence is isMember, a bad hash is "not hex string", a non-integer
// index is "not number", and the uint32 index is checked against the
// validated ledger before the zero check, so a negative one is too large.
func TestLedgerRequest_SelectorErrorsMatchRippled(t *testing.T) {
	mock := &ledgerRequestMock{mockLedgerService: newMockLedgerService()}
	mock.validatedLedgerIndex = 10
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock}),
	}
	const exactlyOne = "Exactly one of 'ledger_hash' or 'ledger_index' can be specified."
	tests := []struct {
		name    string
		params  string
		message string
	}{
		{"none", `{}`, exactlyOne},
		{"both", `{"ledger_index":1,"ledger_hash":"` + strings.Repeat("0", 64) + `"}`, exactlyOne},
		{"empty hash and index", `{"ledger_index":1,"ledger_hash":""}`, exactlyOne},
		{"empty hash", `{"ledger_hash":""}`, "Invalid field 'ledger_hash', not hex string."},
		{"short hash", `{"ledger_hash":"x"}`, "Invalid field 'ledger_hash', not hex string."},
		{"numeric hash", `{"ledger_hash":5}`, "Invalid field 'ledger_hash', not hex string."},
		{"string index", `{"ledger_index":"1"}`, "Invalid field 'ledger_index', not number."},
		{"null index", `{"ledger_index":null}`, "Invalid field 'ledger_index', not number."},
		{"negative index", `{"ledger_index":-1}`, "Ledger index too large"},
		{"zero index", `{"ledger_index":0}`, "Ledger index too small"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx, json.RawMessage(test.params))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, test.message, rpcErr.Message)
		})
	}
}
