package rpc

import (
	"encoding/json"
	"fmt"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

func TestTxMethodUnavailableLedgerDoesNotReturnRelayCache(t *testing.T) {
	db, err := sqlitedb.NewRepositoryManager(t.Context(), t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := service.Config{
		Standalone: true, GenesisConfig: genesis.DefaultConfig(),
		RelationalDB: db, LedgerCacheSize: 2,
	}
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	transaction := payment.Pay(jtx.MasterAccount(), jtx.NewAccount("unavailable-tx"), 50_000_000).Sequence(1).Build()
	env.SignWith(transaction, jtx.MasterAccount())
	fields, err := transaction.Flatten()
	require.NoError(t, err)
	blob, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	submitted, err := svc.SubmitTransaction(transaction, blob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, submitted.Result)
	require.True(t, submitted.Applied)

	ctx := &types.RpcContext{
		Context: t.Context(), Role: types.RoleAdmin,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{Ledger: rpcadapter.NewLedgerServiceAdapter(svc)}),
	}
	check := func(validated, missing bool, sequence uint32) {
		t.Helper()
		for _, version := range []int{types.ApiVersion1, types.ApiVersion2} {
			for _, binary := range []bool{false, true} {
				for _, ranged := range []bool{false, true} {
					name := fmt.Sprintf("%s/ranged=%t/validated=%t/missing=%t", apiVersionName(version, binary), ranged, validated, missing)
					t.Run(name, func(t *testing.T) {
						ctx.ApiVersion = version
						params := map[string]any{"transaction": protocol.Hash256Hex(hash), "binary": binary}
						if ranged {
							params["min_ledger"], params["max_ledger"] = sequence, sequence+1
						}
						raw, err := json.Marshal(params)
						require.NoError(t, err)
						result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, raw)
						if missing {
							require.Nil(t, result)
							require.NotNil(t, rpcErr)
							require.Equal(t, rpcerrors.RpcTXN_NOT_FOUND, rpcErr.Code)
							require.Equal(t, "txnNotFound", rpcErr.ErrorString)
							if ranged {
								require.Equal(t, false, rpcErr.Extra["searched_all"])
							} else {
								require.NotContains(t, rpcErr.Extra, "searched_all")
							}
							return
						}
						require.Nil(t, rpcErr)
						response := result.(map[string]any)
						require.Equal(t, validated, response["validated"])
						require.Equal(t, protocol.Hash256Hex(hash), response["hash"])
						if validated {
							require.Equal(t, sequence, response["ledger_index"])
							if binary {
								metadataField := "meta"
								if version > 1 {
									metadataField = "meta_blob"
								}
								metadata, ok := response[metadataField].(string)
								require.True(t, ok, "validated binary response missing %s", metadataField)
								require.NotEmpty(t, metadata)
							} else {
								metadata, ok := response["meta"].(map[string]any)
								require.True(t, ok, "validated JSON response missing metadata")
								require.Equal(t, "tesSUCCESS", metadata["TransactionResult"])
							}
						} else {
							require.NotContains(t, response, "ledger_index")
							require.NotContains(t, response, "meta")
							require.NotContains(t, response, "meta_blob")
						}
					})
				}
			}
		}
	}
	check(false, false, 3)
	sequence, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	check(true, false, sequence)

	for range cfg.LedgerCacheSize {
		_, err := svc.AcceptLedger(t.Context())
		require.NoError(t, err)
	}
	require.False(t, svc.HasCompleteLedger(sequence))
	require.Equal(t, fmt.Sprintf("%d-%d", sequence+1, sequence+cfg.LedgerCacheSize), svc.GetServerInfo().CompleteLedgers)
	check(true, false, sequence)
	require.NoError(t, db.Transaction().DeleteTransactionsBeforeLedgerSeq(t.Context(), relationaldb.LedgerIndex(sequence+1)))
	require.NoError(t, db.Ledger().DeleteLedgersBySeq(t.Context(), relationaldb.LedgerIndex(sequence)))

	retained, included, _, found := svc.TransactionForRelay(hash)
	require.True(t, found)
	require.False(t, included)
	require.Equal(t, blob, retained)
	check(false, true, sequence)

	resubmitted, err := svc.SubmitTransaction(transaction, blob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TefPAST_SEQ, resubmitted.Result)
	require.False(t, resubmitted.Applied)
	check(false, true, sequence)
}

func TestTxMethodFutureSequenceHeldTransactionLifecycle(t *testing.T) {
	db, err := sqlitedb.NewRepositoryManager(t.Context(), t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := service.Config{
		Standalone: true, GenesisConfig: genesis.DefaultConfig(),
		RelationalDB: db, LedgerCacheSize: 2,
	}
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	future := payment.Pay(master, jtx.NewAccount("future-sequence"), 50_000_000).Sequence(2).Build()
	env.SignWith(future, master)
	futureBlob, err := tx.SerializeTransaction(future)
	require.NoError(t, err)
	futureHash, err := tx.ComputeTransactionHash(future)
	require.NoError(t, err)

	submitted, err := svc.SubmitTransaction(future, futureBlob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TerPRE_SEQ, submitted.Result)
	require.False(t, submitted.Applied)
	foundOpen, err := svc.OpenLedgerHasTx(futureHash)
	require.NoError(t, err)
	require.False(t, foundOpen)
	for _, candidate := range svc.QueueAllTxs() {
		require.NotEqual(t, futureHash, candidate.TxID, "future-sequence transaction entered TxQ")
	}

	ctx := &types.RpcContext{
		Context: t.Context(), Role: types.RoleAdmin,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{Ledger: rpcadapter.NewLedgerServiceAdapter(svc)}),
	}
	check := func(validated bool, ledgerIndex uint32) {
		t.Helper()
		for _, version := range []int{types.ApiVersion1, types.ApiVersion2} {
			for _, binary := range []bool{false, true} {
				for _, ranged := range []bool{false, true} {
					name := fmt.Sprintf("%s/ranged=%t/validated=%t", apiVersionName(version, binary), ranged, validated)
					t.Run(name, func(t *testing.T) {
						ctx.ApiVersion = version
						params := map[string]any{"transaction": protocol.Hash256Hex(futureHash), "binary": binary}
						if ranged {
							params["min_ledger"], params["max_ledger"] = uint32(1), ledgerIndex+1
						}
						raw, err := json.Marshal(params)
						require.NoError(t, err)
						result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, raw)
						require.Nil(t, rpcErr)
						response := result.(map[string]any)
						require.Equal(t, validated, response["validated"])
						require.Equal(t, protocol.Hash256Hex(futureHash), response["hash"])
						if validated {
							require.Equal(t, ledgerIndex, response["ledger_index"])
							if binary {
								metadataField := "meta"
								if version > 1 {
									metadataField = "meta_blob"
								}
								metadata, ok := response[metadataField].(string)
								require.True(t, ok, "validated binary response missing %s", metadataField)
								require.NotEmpty(t, metadata)
							} else {
								metadata, ok := response["meta"].(map[string]any)
								require.True(t, ok, "validated JSON response missing metadata")
								require.Equal(t, "tesSUCCESS", metadata["TransactionResult"])
							}
						} else {
							require.NotContains(t, response, "ledger_index")
							require.NotContains(t, response, "inLedger")
							require.NotContains(t, response, "meta")
							require.NotContains(t, response, "meta_blob")
						}
						if binary {
							field := "tx"
							if version > 1 {
								field = "tx_blob"
							}
							txBlob, ok := response[field].(string)
							require.True(t, ok, "response missing %s", field)
							require.NotEmpty(t, txBlob)
						} else if version == types.ApiVersion1 {
							require.Equal(t, "Payment", response["TransactionType"])
						} else {
							txJSON, ok := response["tx_json"].(map[string]any)
							require.True(t, ok)
							require.Equal(t, "Payment", txJSON["TransactionType"])
						}
					})
				}
			}
		}
	}
	check(false, 0)

	missing := payment.Pay(master, jtx.NewAccount("missing-sequence"), 50_000_000).Sequence(1).Build()
	env.SignWith(missing, master)
	missingBlob, err := tx.SerializeTransaction(missing)
	require.NoError(t, err)
	missingResult, err := svc.SubmitTransaction(missing, missingBlob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, missingResult.Result)
	require.True(t, missingResult.Applied)
	foundOpen, err = svc.OpenLedgerHasTx(futureHash)
	require.NoError(t, err)
	require.False(t, foundOpen)
	for _, candidate := range svc.QueueAllTxs() {
		require.NotEqual(t, futureHash, candidate.TxID, "future-sequence transaction entered TxQ before retry")
	}

	_, err = svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	validatedSeq, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	check(true, validatedSeq)
}
