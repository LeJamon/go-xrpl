package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type accountValidationOrderService struct {
	*mockLedgerService
	reader types.LedgerReader
}

func (s *accountValidationOrderService) GetLedgerBySequence(uint32) (types.LedgerReader, error) {
	return s.reader, nil
}

func TestAccountHandlerValidationOrder(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	tests := []struct {
		name    string
		method  types.MethodHandler
		params  string
		message string
		code    int
	}{
		{
			name:    "account_offers selector precedes limit",
			method:  &handlers.AccountOffersMethod{},
			params:  `{"account":"` + account + `","ledger_hash":null,"limit":"bad"}`,
			message: "Invalid field 'ledger_hash', not hex string.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account_channels selector precedes destination type",
			method:  &handlers.AccountChannelsMethod{},
			params:  `{"account":"` + account + `","ledger_hash":null,"destination_account":1}`,
			message: "Invalid field 'ledger_hash', not hex string.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account_channels validates destination type after selector",
			method:  &handlers.AccountChannelsMethod{},
			params:  `{"account":"` + account + `","destination_account":1}`,
			message: "Invalid field 'destination_account'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account_info account precedes queue",
			method:  &handlers.AccountInfoMethod{},
			params:  `{"queue":{"bad":true}}`,
			message: "Missing field 'account'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account_nfts malformed account precedes limit",
			method:  &handlers.AccountNftsMethod{},
			params:  `{"account":"bad","limit":"bad"}`,
			message: "Account malformed.",
			code:    types.RpcACT_MALFORMED,
		},
		{
			name:    "account_lines missing account precedes selector",
			method:  &handlers.AccountLinesMethod{},
			params:  `{"ledger_hash":null}`,
			message: "Missing field 'account'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account_lines selector precedes malformed account",
			method:  &handlers.AccountLinesMethod{},
			params:  `{"account":"bad","ledger_hash":null}`,
			message: "Invalid field 'ledger_hash', not hex string.",
			code:    types.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion2,
				Services:   &types.ServiceContainer{Ledger: newMockLedgerService()},
			}
			result, rpcErr := tc.method.Handle(ctx, json.RawMessage(tc.params))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.code, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

func TestAccountInfoQueueAndSignerValidationFollowAccountLookup(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	newContext := func(accountErr error) *types.RPCContext {
		service := &accountValidationOrderService{
			mockLedgerService: newMockLedgerService(),
			reader:            &mockLedgerReader{seq: 2, closed: true, validated: true},
		}
		service.accountInfoErr = accountErr
		return &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   &types.ServiceContainer{Ledger: service},
		}
	}

	for _, field := range []string{"queue", "signer_lists"} {
		t.Run(field+" follows missing SLE", func(t *testing.T) {
			params := json.RawMessage(`{"account":"` + account + `","ledger_index":"validated","` + field + `":"yes"}`)
			result, rpcErr := (&handlers.AccountInfoMethod{}).Handle(newContext(svcerr.ErrAccountNotFound), params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcACT_NOT_FOUND, rpcErr.Code)
			assert.Equal(t, "Account not found.", rpcErr.Message)
		})
	}

	t.Run("truthy queue rejects closed ledger after SLE", func(t *testing.T) {
		params := json.RawMessage(`{"account":"` + account + `","ledger_index":"validated","queue":"yes"}`)
		result, rpcErr := (&handlers.AccountInfoMethod{}).Handle(newContext(nil), params)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid parameters.", rpcErr.Message)
	})

	t.Run("api v2 signer type rejects after SLE", func(t *testing.T) {
		params := json.RawMessage(`{"account":"` + account + `","ledger_index":"validated","signer_lists":"yes"}`)
		result, rpcErr := (&handlers.AccountInfoMethod{}).Handle(newContext(nil), params)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid parameters.", rpcErr.Message)
	})
}

func TestAccountHandlerOptionalValidationFollowsAccountSLE(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	tests := []struct {
		name   string
		method types.MethodHandler
		params string
	}{
		{
			name:   "account_offers limit",
			method: &handlers.AccountOffersMethod{},
			params: `{"account":"` + account + `","limit":"bad"}`,
		},
		{
			name:   "account_channels destination",
			method: &handlers.AccountChannelsMethod{},
			params: `{"account":"` + account + `","destination_account":1}`,
		},
		{
			name:   "account_nfts limit",
			method: &handlers.AccountNftsMethod{},
			params: `{"account":"` + account + `","limit":"bad"}`,
		},
		{
			name:   "account_lines peer",
			method: &handlers.AccountLinesMethod{},
			params: `{"account":"` + account + `","peer":[]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newMockLedgerService()
			service.accountInfoErr = svcerr.ErrAccountNotFound
			ctx := &types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion2,
				Services:   &types.ServiceContainer{Ledger: service},
			}
			result, rpcErr := tc.method.Handle(ctx, json.RawMessage(tc.params))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcACT_NOT_FOUND, rpcErr.Code)
			assert.Equal(t, "Account not found.", rpcErr.Message)
		})
	}
}
