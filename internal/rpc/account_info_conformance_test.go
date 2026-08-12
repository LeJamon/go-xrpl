package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const accountInfoConformanceAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

type accountInfoConformanceLedger struct {
	types.LedgerReader
	rules *amendment.Rules
}

func (l *accountInfoConformanceLedger) LedgerAmendmentRules() *amendment.Rules {
	return l.rules
}

type accountInfoConformanceService struct {
	*mockLedgerService
	ledger types.LedgerReader
}

func (s *accountInfoConformanceService) GetLedgerBySequence(uint32) (types.LedgerReader, error) {
	return s.ledger, nil
}

func (s *accountInfoConformanceService) GetLedgerByHash([32]byte) (types.LedgerReader, error) {
	return s.ledger, nil
}

func newAccountInfoConformanceContext(info *types.AccountInfo, rules *amendment.Rules, closed bool) (*types.RpcContext, *accountInfoConformanceService, [32]byte) {
	var hash [32]byte
	hash[0] = 0xAB
	hash[31] = 0xCD
	reader := &accountInfoConformanceLedger{
		LedgerReader: &mockLedgerReader{
			seq:       2,
			hash:      hash,
			closed:    closed,
			validated: closed,
		},
		rules: rules,
	}
	mock := newMockLedgerService()
	mock.accountInfo = info
	service := &accountInfoConformanceService{mockLedgerService: mock, ledger: reader}
	return &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
		Services:   newTestServices(service),
	}, service, hash
}

func accountInfoConformanceParams(t *testing.T, extra map[string]any) json.RawMessage {
	t.Helper()
	params := map[string]any{
		"account":      accountInfoConformanceAccount,
		"ledger_index": "validated",
	}
	for key, value := range extra {
		params[key] = value
	}
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	return raw
}

func TestAccountInfoRetiredAndAmendmentGatedFlags(t *testing.T) {
	tests := []struct {
		name            string
		rules           *amendment.Rules
		flags           uint32
		wantClawback    bool
		wantClawbackSet bool
		wantLocking     bool
		wantLockingSet  bool
	}{
		{name: "retired clawback flag is always emitted when false", rules: amendment.EmptyRules(), wantClawbackSet: true},
		{
			name:            "retired clawback flag is always emitted when true",
			rules:           amendment.EmptyRules(),
			flags:           entry.LsfAllowTrustLineClawback | entry.LsfAllowTrustLineLocking,
			wantClawback:    true,
			wantClawbackSet: true,
		},
		{
			name:            "TokenEscrow additionally emits locking when true",
			rules:           amendment.NewRules([][32]byte{amendment.FeatureTokenEscrow}),
			flags:           entry.LsfAllowTrustLineClawback | entry.LsfAllowTrustLineLocking,
			wantClawback:    true,
			wantClawbackSet: true,
			wantLocking:     true,
			wantLockingSet:  true,
		},
		{
			name:            "TokenEscrow emits locking when false",
			rules:           amendment.NewRules([][32]byte{amendment.FeatureTokenEscrow}),
			wantClawbackSet: true,
			wantLockingSet:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := &types.AccountInfo{
				Account:  accountInfoConformanceAccount,
				Balance:  "100000000",
				Flags:    test.flags,
				Sequence: 1,
			}
			ctx, _, _ := newAccountInfoConformanceContext(info, test.rules, true)
			result, rpcErr := (&handlers.AccountInfoMethod{}).Handle(ctx, accountInfoConformanceParams(t, nil))
			require.Nil(t, rpcErr)

			flags := result.(map[string]any)["account_flags"].(map[string]bool)
			clawback, clawbackSet := flags["allowTrustLineClawback"]
			locking, lockingSet := flags["allowTrustLineLocking"]
			assert.Equal(t, test.wantClawbackSet, clawbackSet)
			assert.Equal(t, test.wantClawback, clawback)
			assert.Equal(t, test.wantLockingSet, lockingSet)
			assert.Equal(t, test.wantLocking, locking)
		})
	}
}

func TestAccountInfoLookupResultConformance(t *testing.T) {
	t.Run("malformed account retains lookup fields", func(t *testing.T) {
		ctx, _, hash := newAccountInfoConformanceContext(nil, amendment.EmptyRules(), true)
		params := accountInfoConformanceParams(t, map[string]any{"account": "not-an-account"})

		_, rpcErr := (&handlers.AccountInfoMethod{}).Handle(ctx, params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcACT_MALFORMED, rpcErr.Code)
		assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
		assert.Equal(t, handlers.FormatLedgerHash(hash), rpcErr.Extra["ledger_hash"])
		assert.Equal(t, true, rpcErr.Extra["validated"])
		assert.NotContains(t, rpcErr.Extra, "account")
	})

	t.Run("missing AccountRoot adds canonical account to lookup fields", func(t *testing.T) {
		ctx, service, hash := newAccountInfoConformanceContext(nil, amendment.EmptyRules(), true)
		service.accountInfoErr = svcerr.ErrAccountNotFound

		_, rpcErr := (&handlers.AccountInfoMethod{}).Handle(ctx, accountInfoConformanceParams(t, nil))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcACT_NOT_FOUND, rpcErr.Code)
		assert.Equal(t, accountInfoConformanceAccount, rpcErr.Extra["account"])
		assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
		assert.Equal(t, handlers.FormatLedgerHash(hash), rpcErr.Extra["ledger_hash"])
	})

	t.Run("queue on closed ledger retains only lookup fields", func(t *testing.T) {
		info := &types.AccountInfo{Account: accountInfoConformanceAccount, Balance: "1", Sequence: 1}
		ctx, _, _ := newAccountInfoConformanceContext(info, amendment.EmptyRules(), true)

		_, rpcErr := (&handlers.AccountInfoMethod{}).Handle(ctx, accountInfoConformanceParams(t, map[string]any{"queue": true}))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
		assert.NotContains(t, rpcErr.Extra, "account_data")
		assert.NotContains(t, rpcErr.Extra, "account_flags")
	})
}

func TestAccountInfoInvalidSignerListsPartialResult(t *testing.T) {
	const designator = "AE0A97F385FFE42E3096BB352C7A2AA5B0E82C90D80E4DFD0EAABD5B2E4B6E1F"
	encoded, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType": "AccountRoot",
		"Account":         accountInfoConformanceAccount,
		"Balance":         "100000000",
		"Flags":           uint32(0),
		"OwnerCount":      uint32(0),
		"Sequence":        uint32(1),
		"VaultID":         designator,
	})
	require.NoError(t, err)
	raw, err := hex.DecodeString(encoded)
	require.NoError(t, err)

	info := &types.AccountInfo{
		Account:  accountInfoConformanceAccount,
		Balance:  "100000000",
		Sequence: 1,
		RawData:  raw,
	}
	ctx, _, _ := newAccountInfoConformanceContext(info, amendment.EmptyRules(), true)
	_, rpcErr := (&handlers.AccountInfoMethod{}).Handle(ctx, accountInfoConformanceParams(t, map[string]any{"signer_lists": "true"}))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Contains(t, rpcErr.Extra, "account_data")
	assert.Contains(t, rpcErr.Extra, "account_flags")
	assert.Equal(t, map[string]any{"type": "Vault"}, rpcErr.Extra["pseudo_account"])
	assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
	assert.NotContains(t, rpcErr.Extra, "signer_lists")
}

func TestAccountInfoUsesReaderIdentityAndAddsGravatarURL(t *testing.T) {
	info := &types.AccountInfo{
		Account:     accountInfoConformanceAccount,
		Balance:     "100000000",
		Sequence:    1,
		EmailHash:   "98B8A86C8E1F7E89C04AB4AD8ECB8621",
		LedgerIndex: 999,
		LedgerHash:  "DEADBEEF",
		Validated:   false,
	}
	ctx, _, hash := newAccountInfoConformanceContext(info, amendment.EmptyRules(), true)
	result, rpcErr := (&handlers.AccountInfoMethod{}).Handle(ctx, accountInfoConformanceParams(t, nil))
	require.Nil(t, rpcErr)

	response := result.(map[string]any)
	assert.Equal(t, uint32(2), response["ledger_index"])
	assert.Equal(t, handlers.FormatLedgerHash(hash), response["ledger_hash"])
	assert.Equal(t, true, response["validated"])
	accountData := response["account_data"].(map[string]any)
	assert.Equal(t, "https://www.gravatar.com/avatar/98b8a86c8e1f7e89c04ab4ad8ecb8621", accountData["urlgravatar"])
}
