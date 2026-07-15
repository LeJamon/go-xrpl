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

const accountPageTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

type accountPageMethod interface {
	Handle(*types.RpcContext, json.RawMessage) (any, *types.RpcError)
}

func accountPageContext(services *types.ServiceContainer) *types.RpcContext {
	return &types.RpcContext{
		Context: context.Background(), ApiVersion: types.ApiVersion1,
		Role: types.RoleGuest, Services: services,
	}
}

func accountPageCall(t *testing.T, method accountPageMethod, ctx *types.RpcContext, params map[string]any) *types.RpcError {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	_, rpcErr := method.Handle(ctx, raw)
	require.NotNil(t, rpcErr)
	return rpcErr
}

func TestAccountPageLedgerLookupPrecedesAddressAndFilterValidation(t *testing.T) {
	t.Run("account_lines peer", func(t *testing.T) {
		mock := newMockAccountLinesLedgerService()
		mock.accountInfoErr = svcerr.ErrLedgerNotFound
		rpcErr := accountPageCall(t, &handlers.AccountLinesMethod{}, accountPageContext(newAccountLinesTestServices(mock)), map[string]any{
			"account": accountPageTestAccount, "peer": "bad", "ledger_index": 99,
		})
		assert.Equal(t, "lgrNotFound", rpcErr.ErrorString)
	})

	t.Run("account_offers account", func(t *testing.T) {
		mock := newAccountOffersMock()
		mock.accountInfoErr = svcerr.ErrLedgerNotFound
		rpcErr := accountPageCall(t, &handlers.AccountOffersMethod{}, accountPageContext(newAccountOffersTestServices(mock)), map[string]any{
			"account": "bad", "ledger_index": 99,
		})
		assert.Equal(t, "lgrNotFound", rpcErr.ErrorString)
	})

	t.Run("account_channels destination", func(t *testing.T) {
		mock := newMockAccountChannelsLedgerService()
		mock.accountInfoErr = svcerr.ErrLedgerNotFound
		rpcErr := accountPageCall(t, &handlers.AccountChannelsMethod{}, accountPageContext(newAccountChannelsTestServices(mock)), map[string]any{
			"account": accountPageTestAccount, "destination_account": "bad", "ledger_index": 99,
		})
		assert.Equal(t, "lgrNotFound", rpcErr.ErrorString)
	})

	t.Run("account_objects account", func(t *testing.T) {
		mock := newAccountObjectsMock()
		mock.accountInfoErr = svcerr.ErrLedgerNotFound
		rpcErr := accountPageCall(t, &handlers.AccountObjectsMethod{}, accountPageContext(newAccountObjectsTestServices(mock)), map[string]any{
			"account": "bad", "ledger_index": 99,
		})
		assert.Equal(t, "lgrNotFound", rpcErr.ErrorString)
	})
}

func TestAccountPageExistencePrecedesPeerAndDestinationValidation(t *testing.T) {
	t.Run("account_lines peer", func(t *testing.T) {
		mock := newMockAccountLinesLedgerService()
		mock.accountInfoErr = svcerr.ErrAccountNotFound
		rpcErr := accountPageCall(t, &handlers.AccountLinesMethod{}, accountPageContext(newAccountLinesTestServices(mock)), map[string]any{
			"account": accountPageTestAccount, "peer": "bad",
		})
		assert.Equal(t, "actNotFound", rpcErr.ErrorString)
	})

	t.Run("account_channels destination", func(t *testing.T) {
		mock := newMockAccountChannelsLedgerService()
		mock.accountInfoErr = svcerr.ErrAccountNotFound
		rpcErr := accountPageCall(t, &handlers.AccountChannelsMethod{}, accountPageContext(newAccountChannelsTestServices(mock)), map[string]any{
			"account": accountPageTestAccount, "destination_account": "bad",
		})
		assert.Equal(t, "actNotFound", rpcErr.ErrorString)
	})
}

func TestAccountPageLimitValidationPrecedesMarker(t *testing.T) {
	tests := []struct {
		name   string
		method accountPageMethod
		ctx    *types.RpcContext
	}{
		{"account_lines", &handlers.AccountLinesMethod{}, accountPageContext(newAccountLinesTestServices(newMockAccountLinesLedgerService()))},
		{"account_offers", &handlers.AccountOffersMethod{}, accountPageContext(newAccountOffersTestServices(newAccountOffersMock()))},
		{"account_channels", &handlers.AccountChannelsMethod{}, accountPageContext(newAccountChannelsTestServices(newMockAccountChannelsLedgerService()))},
		{"account_objects", &handlers.AccountObjectsMethod{}, accountPageContext(newAccountObjectsTestServices(newAccountObjectsMock()))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rpcErr := accountPageCall(t, test.method, test.ctx, map[string]any{
				"account": accountPageTestAccount, "limit": 0, "marker": 7,
			})
			assert.Equal(t, "invalidParams", rpcErr.ErrorString)
			assert.Equal(t, "Invalid field 'limit'.", rpcErr.Message)
		})
	}
}

func TestAccountObjectsTypeValidationPrecedesLimit(t *testing.T) {
	mock := newAccountObjectsMock()
	rpcErr := accountPageCall(t, &handlers.AccountObjectsMethod{}, accountPageContext(newAccountObjectsTestServices(mock)), map[string]any{
		"account": accountPageTestAccount, "type": "not_a_ledger_type", "limit": 0,
	})
	assert.Equal(t, "Invalid field 'type'.", rpcErr.Message)
}
