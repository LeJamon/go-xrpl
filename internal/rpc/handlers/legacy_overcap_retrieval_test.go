package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

func TestLegacyOverCapEscrowRetrievalAfterServiceClose(t *testing.T) {
	destination := jtx.NewAccount("legacy-overcap-retrieval-destination")
	genesisConfig := genesis.DefaultConfig()
	filteredAmendments := make([][32]byte, 0, len(genesisConfig.Amendments)-1)
	for _, feature := range genesisConfig.Amendments {
		if feature != amendment.FeatureFixCleanup3_2_0 {
			filteredAmendments = append(filteredAmendments, feature)
		}
	}
	genesisConfig.Amendments = filteredAmendments
	genesisConfig.InitialAccounts = []genesis.InitialAccount{{
		Address: destination.Address,
		Balance: 1_000_000_000,
	}}

	rm, err := sqlitedb.NewRepositoryManager(t.Context(), t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rm.Close() })
	svc, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesisConfig,
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnvWithConfig(t, genesisConfig)
	env.SetVerifySignatures(true)
	sender := jtx.MasterAccount()
	const overCap = uint64(binarytypes.MaxDrops) + 1
	sequence := uint32(1)
	finishAfter := ^uint32(0)
	txn := escrow.NewEscrowCreate(
		sender.Address,
		destination.Address,
		tx.NewXRPAmount(int64(overCap)),
	)
	txn.Fee = "10"
	txn.Sequence = &sequence
	txn.FinishAfter = &finishAfter
	env.SignWith(txn, sender)

	blob, err := tx.SerializeTransaction(txn)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(txn)
	require.NoError(t, err)
	hashString := strings.ToUpper(hex.EncodeToString(hash[:]))

	outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.NoError(t, err)
	require.Equal(t, openledger.ResultSuccess, outcome.Class)
	require.True(t, outcome.Applied)
	require.Equal(t, ter.TecUNFUNDED, outcome.Result)
	require.Equal(t, uint64(10), outcome.Fee)
	require.NotNil(t, outcome.Metadata)

	openResult, err := svc.GetTransaction(hash)
	require.NoError(t, err)
	openTx, openMeta, err := tx.SplitTxWithMetaBlob(openResult.TxData)
	require.NoError(t, err)
	require.Equal(t, blob, openTx)
	require.Nil(t, openMeta)
	openFields, err := binarycodec.DecodeBytesAllowOverCap(openTx)
	require.NoError(t, err)
	assertLegacyOverCapAmount(t, openFields, overCap)

	adapter := rpcadapter.NewLedgerServiceAdapter(svc)
	ctx := &types.RpcContext{
		Context:    t.Context(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: adapter}),
	}
	query := func(binary bool) map[string]any {
		t.Helper()
		params, marshalErr := json.Marshal(map[string]any{
			"transaction": hashString,
			"binary":      binary,
		})
		require.NoError(t, marshalErr)
		response, rpcErr := (&TxMethod{}).Handle(ctx, params)
		require.Nil(t, rpcErr)
		responseMap, ok := response.(map[string]any)
		require.True(t, ok)
		return responseMap
	}

	openJSON := query(false)
	require.Equal(t, false, openJSON["validated"])
	openTxJSON, ok := openJSON["tx_json"].(map[string]any)
	require.True(t, ok)
	assertLegacyOverCapAmount(t, openTxJSON, overCap)
	openJSONBytes, err := json.Marshal(openJSON)
	require.NoError(t, err)
	require.Contains(t, string(openJSONBytes), `"Amount":"100000000000000001"`)
	require.NotContains(t, openJSON, "meta")
	openBinary := query(true)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(blob)), openBinary["tx_blob"])
	require.NotContains(t, openBinary, "meta_blob")

	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	closedSequence, err := svc.AcceptConsensusResult(t.Context(), parent, [][]byte{blob}, nil, time.Now(), true)
	require.NoError(t, err)
	closedLedger := svc.GetClosedLedger()
	require.NotNil(t, closedLedger)
	svc.SetValidatedLedger(closedSequence, closedLedger.Hash())

	closedResult, err := svc.GetTransaction(hash)
	require.NoError(t, err)
	require.Equal(t, closedSequence, closedResult.LedgerIndex)
	require.True(t, closedResult.Validated)
	closedTx, closedMeta, err := tx.SplitTxWithMetaBlobStrict(closedResult.TxData)
	require.NoError(t, err)
	require.Equal(t, blob, closedTx)
	require.NotEmpty(t, closedMeta)
	closedFields, err := binarycodec.DecodeBytesAllowOverCap(closedTx)
	require.NoError(t, err)
	assertLegacyOverCapAmount(t, closedFields, overCap)
	metadata, err := binarycodec.DecodeBytes(closedMeta)
	require.NoError(t, err)
	require.Equal(t, "tecUNFUNDED", metadata["TransactionResult"])
	require.Equal(t, closedResult.TxIndex, metadata["TransactionIndex"])

	closedJSON := query(false)
	require.Equal(t, true, closedJSON["validated"])
	closedTxJSON, ok := closedJSON["tx_json"].(map[string]any)
	require.True(t, ok)
	assertLegacyOverCapAmount(t, closedTxJSON, overCap)
	closedJSONBytes, err := json.Marshal(closedJSON)
	require.NoError(t, err)
	require.Contains(t, string(closedJSONBytes), `"Amount":"100000000000000001"`)
	closedMetaJSON, ok := closedJSON["meta"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tecUNFUNDED", closedMetaJSON["TransactionResult"])
	closedBinary := query(true)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(blob)), closedBinary["tx_blob"])
	require.NotEmpty(t, closedBinary["meta_blob"])
}

func assertLegacyOverCapAmount(t *testing.T, fields map[string]any, want uint64) {
	t.Helper()
	amount, ok := fields["Amount"].(binarytypes.RawXRPAmount)
	require.True(t, ok, "Amount type = %T", fields["Amount"])
	require.Equal(t, want, amount.Drops)
}
