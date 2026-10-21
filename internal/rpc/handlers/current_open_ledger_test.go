package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

func TestLedgerCurrentOpenAcceptedPaymentQueries(t *testing.T) {
	for _, ingress := range []string{"rpc", "open-ledger"} {
		t.Run(ingress, func(t *testing.T) {
			svc, err := service.New(service.Config{
				Standalone:    true,
				GenesisConfig: genesis.DefaultConfig(),
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)

			env := jtx.NewTestEnv(t)
			env.SetVerifySignatures(true)
			sender := jtx.MasterAccount()
			receiver := jtx.NewAccount("current-ledger-rpc")
			txn := payment.Pay(sender, receiver, 100_000_000).Fee(10).Sequence(1).Build()
			env.SignWith(txn, sender)
			txJSON, err := txn.Flatten()
			require.NoError(t, err)
			hexBlob, err := binarycodec.Encode(txJSON)
			require.NoError(t, err)
			blob, err := hex.DecodeString(hexBlob)
			require.NoError(t, err)
			hash, err := tx.ComputeTransactionHash(txn)
			require.NoError(t, err)
			hashString := strings.ToUpper(hex.EncodeToString(hash[:]))

			if ingress == "rpc" {
				parsed, parseErr := tx.ParseFromBinary(blob)
				require.NoError(t, parseErr)
				result, submitErr := svc.SubmitTransaction(parsed, blob, false)
				require.NoError(t, submitErr)
				require.Equal(t, ter.TesSUCCESS, result.Result)
				require.True(t, result.Applied)
			} else {
				result, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, true)
				require.NoError(t, submitErr)
				require.Equal(t, openledger.ResultSuccess, result.Class)
				require.True(t, result.Applied)
			}
			present, err := svc.OpenLedgerHasTx(hash)
			require.NoError(t, err)
			require.True(t, present)

			adapter := rpcadapter.NewLedgerServiceAdapter(svc)
			ctx := &types.RpcContext{
				Context:    t.Context(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion1,
				Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: adapter}),
			}
			currentSequence := svc.GetCurrentLedgerIndex()
			closedSequence := svc.GetClosedLedgerIndex()
			for _, selector := range []string{"current", strconv.FormatUint(uint64(currentSequence), 10)} {
				for _, expand := range []bool{false, true} {
					params, marshalErr := json.Marshal(map[string]any{
						"ledger_index": selector,
						"transactions": true,
						"expand":       expand,
					})
					require.NoError(t, marshalErr)
					response, rpcErr := (&LedgerMethod{}).Handle(ctx, params)
					require.Nil(t, rpcErr)
					transactions := ledgerTransactions(t, response)
					require.Len(t, transactions, 1, "selector=%s expand=%t", selector, expand)
					if !expand {
						require.Equal(t, hashString, transactions[0])
						continue
					}
					entry := transactions[0].(map[string]any)
					require.Equal(t, hashString, entry["hash"])
					require.Equal(t, "Payment", entry["TransactionType"])
					require.Equal(t, sender.Address, entry["Account"])
					require.NotContains(t, entry, "metaData")
					require.NotContains(t, entry, "meta")
					require.NotContains(t, entry, "meta_blob")
				}
			}

			for _, params := range []string{
				`{"account":"` + sender.Address + `"}`,
				`{"account":"` + sender.Address + `","ledger_index":"current"}`,
			} {
				response, rpcErr := (&AccountInfoMethod{}).Handle(ctx, json.RawMessage(params))
				require.Nil(t, rpcErr)
				responseMap := response.(map[string]any)
				accountData := responseMap["account_data"].(map[string]any)
				require.EqualValues(t, 2, accountData["Sequence"], "params=%s", params)
				require.EqualValues(t, currentSequence, responseMap["ledger_current_index"], "params=%s", params)
			}

			for _, selector := range []string{"closed", strconv.FormatUint(uint64(closedSequence), 10)} {
				params, marshalErr := json.Marshal(map[string]any{
					"ledger_index": selector,
					"transactions": true,
					"expand":       true,
				})
				require.NoError(t, marshalErr)
				response, rpcErr := (&LedgerMethod{}).Handle(ctx, params)
				require.Nil(t, rpcErr)
				require.Empty(t, ledgerTransactions(t, response), "selector=%s", selector)
			}

			_, err = svc.AcceptLedger(t.Context())
			require.NoError(t, err)
			newClosedSequence := svc.GetClosedLedgerIndex()
			require.Equal(t, currentSequence, newClosedSequence)
			params, marshalErr := json.Marshal(map[string]any{
				"ledger_index": "closed",
				"transactions": true,
				"expand":       true,
			})
			require.NoError(t, marshalErr)
			response, rpcErr := (&LedgerMethod{}).Handle(ctx, params)
			require.Nil(t, rpcErr)
			transactions := ledgerTransactions(t, response)
			require.Len(t, transactions, 1)
			closedEntry := transactions[0].(map[string]any)
			require.Equal(t, hashString, closedEntry["hash"])
			require.Contains(t, closedEntry, "metaData")
		})
	}
}

func TestRawOpenTransactionQueriesThroughRPC(t *testing.T) {
	rm, err := sqlitedb.NewRepositoryManager(t.Context(), t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rm.Close() })
	svc, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	sender := jtx.MasterAccount()
	txn := payment.Pay(sender, jtx.NewAccount("raw-open-rpc"), 1_000_000).Fee(10).Sequence(1).Build()
	env.SignWith(txn, sender)
	txJSON, err := txn.Flatten()
	require.NoError(t, err)
	hexBlob, err := binarycodec.Encode(txJSON)
	require.NoError(t, err)
	raw, err := hex.DecodeString(hexBlob)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(txn)
	require.NoError(t, err)
	hashString := strings.ToUpper(hex.EncodeToString(hash[:]))
	require.NoError(t, svc.GetOpenLedger().AddTransaction(hash, raw))

	adapter := rpcadapter.NewLedgerServiceAdapter(svc)
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		ctx := &types.RpcContext{
			Context:    t.Context(),
			Role:       types.RoleGuest,
			ApiVersion: apiVersion,
			Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: adapter}),
		}
		for _, binary := range []bool{false, true} {
			params, marshalErr := json.Marshal(map[string]any{
				"transaction": hashString,
				"binary":      binary,
			})
			require.NoError(t, marshalErr)
			response, rpcErr := (&TxMethod{}).Handle(ctx, params)
			require.Nil(t, rpcErr, "api=%d binary=%t", apiVersion, binary)
			responseMap := response.(map[string]any)
			require.Equal(t, hashString, responseMap["hash"])
			require.Equal(t, false, responseMap["validated"])
			if binary {
				binaryKey := "tx"
				if apiVersion > 1 {
					binaryKey = "tx_blob"
				}
				require.Equal(t, strings.ToUpper(hexBlob), responseMap[binaryKey])
				require.NotContains(t, responseMap, "meta")
				require.NotContains(t, responseMap, "meta_blob")
				continue
			}
			if apiVersion == types.ApiVersion1 {
				require.Equal(t, "Payment", responseMap["TransactionType"])
			} else {
				txResponse, ok := responseMap["tx_json"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "Payment", txResponse["TransactionType"])
			}
			require.NotContains(t, responseMap, "meta")
		}

		for _, binary := range []bool{false, true} {
			params, marshalErr := json.Marshal(map[string]any{
				"ledger_index": "current",
				"transactions": true,
				"expand":       true,
				"binary":       binary,
			})
			require.NoError(t, marshalErr)
			response, rpcErr := (&LedgerMethod{}).Handle(ctx, params)
			require.Nil(t, rpcErr, "api=%d binary=%t", apiVersion, binary)
			transactions := ledgerTransactions(t, response)
			require.Len(t, transactions, 1)
			entry := transactions[0].(map[string]any)
			if binary {
				require.Equal(t, strings.ToUpper(hexBlob), entry["tx_blob"])
				require.NotContains(t, entry, "meta_blob")
				continue
			}
			require.Equal(t, hashString, entry["hash"])
			if apiVersion == types.ApiVersion1 {
				require.Equal(t, "Payment", entry["TransactionType"])
			} else {
				txResponse, ok := entry["tx_json"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "Payment", txResponse["TransactionType"])
			}
			require.NotContains(t, entry, "meta")
		}
	}
}

func ledgerTransactions(t *testing.T, response any) []any {
	t.Helper()
	responseMap, ok := response.(map[string]any)
	require.True(t, ok)
	ledgerJSON, ok := responseMap["ledger"].(map[string]any)
	require.True(t, ok)
	transactions, ok := ledgerJSON["transactions"].([]any)
	require.True(t, ok)
	return transactions
}
