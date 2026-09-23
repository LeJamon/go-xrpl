package rpc

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

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

func vaultInfoTestContext(mock *vaultInfoMockLedgerService) (*handlers.VaultInfoMethod, *types.RpcContext) {
	return &handlers.VaultInfoMethod{}, &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock}),
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
		{"no identifying members", `{"ledger_index":"validated"}`, rpcerrors.RpcINVALID_PARAMS, "Must specify either 'vault_id' or both 'owner' and 'seq'.", false, "invalidParams"},
		{"null vault_id counts as present", `{"ledger_index":"validated","vault_id":null}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'vault_id', not hex string.", false, "invalidParams"},
		{"empty vault_id counts as present", `{"ledger_index":"validated","vault_id":""}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'vault_id', not hex string.", false, "invalidParams"},
		{"numeric vault_id is not a string", `{"ledger_index":"validated","vault_id":0}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'vault_id', not hex string.", false, "invalidParams"},
		{"object vault_id is not a string", `{"ledger_index":"validated","vault_id":{}}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'vault_id', not hex string.", false, "invalidParams"},
		{"malformed vault_id string", `{"ledger_index":"validated","vault_id":"foobar"}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'vault_id', not hex string.", false, "invalidParams"},
		{"short nonzero vault_id is malformed", `{"ledger_index":"validated","vault_id":"1"}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'vault_id', not hex string.", false, "invalidParams"},
		{"owner without seq", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `"}`, rpcerrors.RpcINVALID_PARAMS, "Must specify either 'vault_id' or both 'owner' and 'seq'.", false, "invalidParams"},
		{"seq without owner", `{"ledger_index":"validated","seq":1}`, rpcerrors.RpcINVALID_PARAMS, "Must specify either 'vault_id' or both 'owner' and 'seq'.", false, "invalidParams"},
		{"empty owner with seq", `{"ledger_index":"validated","owner":"","seq":1}`, rpcerrors.RpcACT_MALFORMED, "Invalid field 'owner', not AccountID.", false, "actMalformed"},
		{"null owner with seq", `{"ledger_index":"validated","owner":null,"seq":1}`, rpcerrors.RpcACT_MALFORMED, "Invalid field 'owner', not AccountID.", false, "actMalformed"},
		{"object owner with seq", `{"ledger_index":"validated","owner":{},"seq":1}`, rpcerrors.RpcACT_MALFORMED, "Invalid field 'owner', not AccountID.", false, "actMalformed"},
		{"array owner with seq", `{"ledger_index":"validated","owner":[],"seq":1}`, rpcerrors.RpcACT_MALFORMED, "Invalid field 'owner', not AccountID.", false, "actMalformed"},
		{"malformed owner with seq", `{"ledger_index":"validated","owner":"foobar","seq":1}`, rpcerrors.RpcACT_MALFORMED, "Invalid field 'owner', not AccountID.", false, "actMalformed"},
		{"null seq counts as present", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":null}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'seq', not a positive 32-bit integer.", false, "invalidParams"},
		{"zero seq counts as present", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":0}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'seq', not a positive 32-bit integer.", false, "invalidParams"},
		{"real seq is not an integer", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":1.0}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'seq', not a positive 32-bit integer.", false, "invalidParams"},
		{"numeric string seq is not an integer", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":"1"}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'seq', not a positive 32-bit integer.", false, "invalidParams"},
		{"negative seq is not positive", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":-1}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'seq', not a positive 32-bit integer.", false, "invalidParams"},
		{"too-large seq is not 32-bit", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":1e20}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'seq', not a positive 32-bit integer.", false, "invalidParams"},
		{"boolean seq is not an integer", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":true}`, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'seq', not a positive 32-bit integer.", false, "invalidParams"},
		{"empty owner conflicts with vault_id", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `","owner":""}`, rpcerrors.RpcINVALID_PARAMS, "Must specify either 'vault_id' or both 'owner' and 'seq'.", false, "invalidParams"},
		{"null seq conflicts with vault_id", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `","seq":null}`, rpcerrors.RpcINVALID_PARAMS, "Must specify either 'vault_id' or both 'owner' and 'seq'.", false, "invalidParams"},
		{"all three identifying members", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `","owner":"","seq":0}`, rpcerrors.RpcINVALID_PARAMS, "Must specify either 'vault_id' or both 'owner' and 'seq'.", false, "invalidParams"},
		{"zero vault key", `{"ledger_index":"validated","vault_id":"0000000000000000000000000000000000000000000000000000000000000000"}`, rpcerrors.RpcENTRY_NOT_FOUND, "Entry not found.", false, "entryNotFound"},
		{"short zero vault key", `{"ledger_index":"validated","vault_id":"0"}`, rpcerrors.RpcENTRY_NOT_FOUND, "Entry not found.", false, "entryNotFound"},
		{"valid direct form reaches lookup", `{"ledger_index":"validated","vault_id":"` + vaultInfoID + `"}`, rpcerrors.RpcENTRY_NOT_FOUND, "Entry not found.", false, "entryNotFound"},
		{"valid owner seq form reaches lookup", `{"ledger_index":"validated","owner":"` + vaultInfoAccount + `","seq":1}`, rpcerrors.RpcENTRY_NOT_FOUND, "Entry not found.", false, "entryNotFound"},
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
	assert.False(t, rpcErr.IsBareToken())
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
	assert.Equal(t, strings.ToUpper(hex.EncodeToString(vaultKey[:])), vault["index"])
	shares, ok := vault["shares"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "10", shares["OutstandingAmount"])
	assert.Equal(t, strings.ToUpper(hex.EncodeToString(issuanceKey[:])), shares["index"])
	assert.Equal(t, vaultShareMPTID, shares["mpt_issuance_id"])
	assert.NotContains(t, response, "shares")
	require.Len(t, mock.requests, 2)
	assert.Equal(t, vaultKey, mock.requests[0])
	assert.Equal(t, issuanceKey, mock.requests[1])
}

func TestVaultInfoRejectsWrongLedgerEntryTypes(t *testing.T) {
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3} {
		for _, wrongEntry := range []string{"vault", "shares"} {
			t.Run(wrongEntry+"/api"+strconv.Itoa(apiVersion), func(t *testing.T) {
				mock := newVaultInfoMockLedgerService()
				method, ctx := vaultInfoTestContext(mock)
				ctx.ApiVersion = apiVersion
				ownerID := ledgerEntryTestAccountID(t, vaultInfoAccount)
				vaultKey := keylet.Vault(ownerID, 1).Key
				issuanceKey := keylet.MPTIssuance(vaultInfoShareID(t)).Key
				mock.entries[vaultKey] = &types.LedgerEntryResult{
					Node: encodeSyntheticRPCObject(t, map[string]any{
						"LedgerEntryType": "Vault",
						"ShareMPTID":      vaultShareMPTID,
					}),
				}
				wrongKey := issuanceKey
				wantLookups := 2
				if wrongEntry == "vault" {
					vaultKey = keylet.Account(ownerID).Key
					wrongKey = vaultKey
					wantLookups = 1
				}
				mock.entries[wrongKey] = &types.LedgerEntryResult{
					Node: encodeSyntheticRPCObject(t, map[string]any{
						"LedgerEntryType": "AccountRoot",
						"Account":         vaultInfoAccount,
					}),
				}
				result, rpcErr := method.Handle(ctx, []byte(`{"ledger_index":"validated","vault_id":"`+hex.EncodeToString(vaultKey[:])+`"}`))
				require.Nil(t, result)
				require.NotNil(t, rpcErr)
				assert.Equal(t, rpcerrors.RpcENTRY_NOT_FOUND, rpcErr.Code)
				assert.Equal(t, "entryNotFound", rpcErr.ErrorString)
				assert.Equal(t, "Entry not found.", rpcErr.Message)
				assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
				assert.Equal(t, true, rpcErr.Extra["validated"])
				assert.Len(t, mock.requests, wantLookups)
			})
		}
	}
}
