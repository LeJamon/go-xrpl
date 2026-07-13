package rpc

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	vaultInfoAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	vaultInfoID      = "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
	vaultShareMPTID  = "00000001B5F762798A53D543A014CAF8B297CFF8F2F937E8"
)

type vaultInfoMockLedgerService struct {
	*mockLedgerEntryService
	entries  map[[32]byte]*types.LedgerEntryResult
	errors   map[[32]byte]error
	requests [][32]byte
}

func newVaultInfoMockLedgerService() *vaultInfoMockLedgerService {
	return &vaultInfoMockLedgerService{
		mockLedgerEntryService: newMockLedgerEntryService(),
		entries:                make(map[[32]byte]*types.LedgerEntryResult),
		errors:                 make(map[[32]byte]error),
	}
}

func (m *vaultInfoMockLedgerService) GetLedgerEntry(_ context.Context, key [32]byte, _ string) (*types.LedgerEntryResult, error) {
	m.requests = append(m.requests, key)
	if err, ok := m.errors[key]; ok {
		return nil, err
	}
	if result, ok := m.entries[key]; ok {
		return result, nil
	}
	return nil, svcerr.ErrLedgerEntryNotFound
}

func vaultInfoTestContext(mock *vaultInfoMockLedgerService) (*handlers.VaultInfoMethod, *types.RPCContext) {
	return &handlers.VaultInfoMethod{}, &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: mock},
	}
}

func vaultInfoShareID(t *testing.T) [24]byte {
	t.Helper()
	decoded, err := hex.DecodeString(vaultShareMPTID)
	require.NoError(t, err)
	var id [24]byte
	copy(id[:], decoded)
	return id
}

func TestVaultInfoRawMembershipAndMalformedProjection(t *testing.T) {
	tests := []struct {
		name      string
		params    string
		code      int
		message   string
		bare      bool
		wantError string
	}{
		{"no identifying members", `{"ledger_index":"validated"}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"null vault_id counts as present", `{"ledger_index":"validated","vault_id":null}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"empty vault_id counts as present", `{"ledger_index":"validated","vault_id":""}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"owner without seq", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `"}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"seq without owner", `{"ledger_index":"validated","seq":1}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"empty owner with seq", `{"ledger_index":"validated","owner":"","seq":1}`, types.RpcACT_MALFORMED, "Account malformed.", false, "malformedRequest"},
		{"null owner with seq", `{"ledger_index":"validated","owner":null,"seq":1}`, types.RpcACT_MALFORMED, "Account malformed.", false, "malformedRequest"},
		{"null seq counts as present", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":null}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"zero seq counts as present", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":0}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"real seq is not an integer", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":1.0}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"numeric string seq is not an integer", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":"1"}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"empty owner conflicts with vault_id", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `","owner":""}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"null seq conflicts with vault_id", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `","seq":null}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"all three identifying members", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `","owner":"","seq":0}`, types.RpcINVALID_PARAMS, "Invalid parameters.", false, "malformedRequest"},
		{"zero vault key", `{"ledger_index":"validated","vault_id":"0000000000000000000000000000000000000000000000000000000000000000"}`, types.RpcUNKNOWN, "", true, "malformedRequest"},
		{"valid direct form reaches lookup", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `"}`, types.RpcUNKNOWN, "", true, "entryNotFound"},
		{"valid owner seq form reaches lookup", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":1}`, types.RpcUNKNOWN, "", true, "entryNotFound"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := newVaultInfoMockLedgerService()
			method, ctx := vaultInfoTestContext(mock)
			result, rpcErr := method.Handle(ctx, []byte(tc.params))
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.wantError, rpcErr.ErrorString)
			assert.Equal(t, tc.code, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
			assert.Equal(t, tc.bare, rpcErr.IsBareToken())
			assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
			assert.Equal(t, true, rpcErr.Extra["validated"])
			assert.Equal(t, handlers.FormatLedgerHash([32]byte{0x4B, 0xC5, 0x0C, 0x9B}), rpcErr.Extra["ledger_hash"])
			assert.NotContains(t, rpcErr.Extra, "ledger_current_index")
		})
	}
}

func TestVaultInfoRequiresShareIssuance(t *testing.T) {
	mock := newVaultInfoMockLedgerService()
	method, ctx := vaultInfoTestContext(mock)
	var vaultKey [32]byte
	vaultKeyBytes, err := hex.DecodeString(vaultInfoID)
	require.NoError(t, err)
	copy(vaultKey[:], vaultKeyBytes)
	mock.entries[vaultKey] = &types.LedgerEntryResult{
		Node: []byte(`{"LedgerEntryType":"Vault","ShareMPTID":"` + vaultShareMPTID + `"}`),
	}

	result, rpcErr := method.Handle(ctx, []byte(`{"ledger_index":"validated","vault_id":"`+vaultInfoID+`"}`))
	require.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, "entryNotFound", rpcErr.ErrorString)
	assert.True(t, rpcErr.IsBareToken())
	require.Len(t, mock.requests, 2)
	assert.Equal(t, vaultKey, mock.requests[0])
	assert.Equal(t, keylet.MPTIssuance(vaultInfoShareID(t)).Key, mock.requests[1])
	assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
	assert.Equal(t, true, rpcErr.Extra["validated"])
}

func TestVaultInfoProjectsSharesFromResolvedLedger(t *testing.T) {
	mock := newVaultInfoMockLedgerService()
	method, ctx := vaultInfoTestContext(mock)
	ownerID := ledgerEntryTestAccountID(t, vaultInfoAccount)
	vaultKey := keylet.Vault(ownerID, 1).Key
	issuanceKey := keylet.MPTIssuance(vaultInfoShareID(t)).Key
	mock.entries[vaultKey] = &types.LedgerEntryResult{
		LedgerIndex: 99,
		LedgerHash:  [32]byte{0xAA},
		Validated:   true,
		Node: encodeSyntheticRPCObject(t, map[string]any{
			"LedgerEntryType": "Vault",
			"Owner":           vaultInfoAccount,
			"ShareMPTID":      vaultShareMPTID,
		}),
	}
	mock.entries[issuanceKey] = &types.LedgerEntryResult{
		LedgerIndex: 99,
		LedgerHash:  [32]byte{0xAA},
		Validated:   true,
		Node: encodeSyntheticRPCObject(t, map[string]any{
			"LedgerEntryType":   "MPTokenIssuance",
			"Sequence":          uint32(1),
			"Issuer":            vaultInfoAccount,
			"OutstandingAmount": "10",
		}),
	}

	result, rpcErr := method.Handle(ctx, []byte(`{"ledger_index":3,"owner":"`+vaultInfoAccount+`","seq":1}`))
	require.Nil(t, rpcErr)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, uint32(3), response["ledger_current_index"])
	assert.Equal(t, false, response["validated"])
	assert.NotContains(t, response, "ledger_hash")
	assert.NotContains(t, response, "ledger_index")

	vault, ok := response["vault"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, handlers.FormatHash(vaultKey[:]), vault["index"])
	shares, ok := vault["shares"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "10", shares["OutstandingAmount"])
	assert.Equal(t, handlers.FormatHash(issuanceKey[:]), shares["index"])
	assert.Equal(t, vaultShareMPTID, shares["mpt_issuance_id"])
	assert.NotContains(t, response, "shares")
	require.Len(t, mock.requests, 2)
	assert.Equal(t, vaultKey, mock.requests[0])
	assert.Equal(t, issuanceKey, mock.requests[1])
}
