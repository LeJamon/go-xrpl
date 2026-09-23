package rpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestSubmitFeeCalculationFailureResponse(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		for _, code := range []ter.Result{ter.TefEXCEPTION, ter.TefINTERNAL} {
			t.Run(fmt.Sprintf("v%d/%s", version, code), func(t *testing.T) {
				mock := newMockLedgerServiceSubmit()
				mock.submitResult = &types.SubmitResult{
					EngineResult: code.String(), EngineResultCode: int(code), EngineResultMessage: code.Message(),
				}
				ctx := &types.RpcContext{
					Context: context.Background(), Role: types.RoleUser, ApiVersion: version,
					Services: newSubmitTestServices(mock),
				}
				params := rawSubmitParams(t, map[string]any{
					"TransactionType": "AccountSet", "Account": validAccountAddress,
					"Fee": "10", "Sequence": 1,
				}, nil)
				result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
				require.Nil(t, rpcErr)
				response := result.(map[string]any)
				require.Equal(t, code.String(), response["engine_result"])
				require.Equal(t, int(code), response["engine_result_code"])
				require.Equal(t, code.Message(), response["engine_result_message"])
				require.Equal(t, false, response["applied"])
				require.Equal(t, false, response["queued"])
				for _, field := range []string{"account_sequence_next", "account_sequence_available", "open_ledger_cost", "validated_ledger_index"} {
					require.NotContains(t, response, field)
				}
			})
		}
	}
}
