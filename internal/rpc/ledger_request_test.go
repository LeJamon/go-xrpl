package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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
	return nil, errors.New("not found")
}

func (m *ledgerRequestMock) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if l, ok := m.bySeq[seq]; ok {
		return l, nil
	}
	return nil, errors.New("not found")
}

func TestLedgerRequest_ServesLocalLedgerByHash(t *testing.T) {
	var hash [32]byte
	hash[0], hash[31] = 0xAB, 0xCD
	lr := &mockLedgerReader{seq: 5, hash: hash, closed: true, validated: true, totalDrops: 99000000}

	mock := &ledgerRequestMock{
		mockLedgerService: newMockLedgerService(),
		byHash:            map[[32]byte]types.LedgerReader{hash: lr},
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: mock},
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_hash":"`+hex.EncodeToString(hash[:])+`"}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, uint32(5), resp["ledger_index"])
	ledger, ok := resp["ledger"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, strings.ToUpper(hex.EncodeToString(hash[:])), ledger["ledger_hash"])
	assert.Equal(t, "5", ledger["ledger_index"])
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
		Services:   &types.ServiceContainer{Ledger: mock},
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_index": 1}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, uint32(1), resp["ledger_index"])
}

func TestLedgerRequest_AcquiringReturnsSnapshotUnderLgrNotFound(t *testing.T) {
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
		Services: &types.ServiceContainer{
			Ledger: mock,
			RequestLedger: func(h [32]byte, seq uint32) (map[string]any, bool) {
				gotHash = h
				return acquiring, true
			},
		},
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_hash":"`+hexHash+`"}`))
	// rippled returns this as a result body (error + acquiring members), not a
	// transport-level error.
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "lgrNotFound", resp["error"])
	assert.Equal(t, acquiring, resp["acquiring"])
	assert.Equal(t, hash, gotHash, "the requested hash must be forwarded to the acquisition coordinator")
}

func TestLedgerRequest_NotFoundWithoutSubsystem(t *testing.T) {
	var hash [32]byte
	hash[0] = 0x77
	mock := &ledgerRequestMock{mockLedgerService: newMockLedgerService()}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: mock}, // RequestLedger nil
	}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(ctx,
		json.RawMessage(`{"ledger_hash":"`+hex.EncodeToString(hash[:])+`"}`))
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
}
