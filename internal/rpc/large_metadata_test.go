package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestCommittedLargeMetadataRetrieval(t *testing.T) {
	const nodeCount = 533
	offers := make([]any, 500)
	for i := range offers {
		offers[i] = fmt.Sprintf("%064X", i+1)
	}
	txJSON := map[string]any{
		"TransactionType": "NFTokenCancelOffer",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":             "10",
		"Sequence":        uint32(502),
		"SigningPubKey":   "",
		"NFTokenOffers":   offers,
	}
	nodes := make([]tx.AffectedNode, nodeCount)
	for i := range nodes {
		nodes[i] = tx.AffectedNode{
			NodeType:        "DeletedNode",
			LedgerEntryType: "NFTokenOffer",
			LedgerIndex:     fmt.Sprintf("%064X", i+1),
		}
	}
	meta := &tx.Metadata{TransactionResult: ter.TesSUCCESS, AffectedNodes: nodes}
	txBlob, err := binarycodec.EncodeBytes(txJSON)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(txBlob)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(parsed)
	require.NoError(t, err)
	hashString := strings.ToUpper(hex.EncodeToString(hash[:]))
	metaBlob, err := tx.SerializeMetadata(meta)
	require.NoError(t, err)
	wantMeta, err := binarycodec.DecodeBytes(metaBlob)
	require.NoError(t, err)
	wantMetaJSON, err := json.Marshal(wantMeta)
	require.NoError(t, err)
	leaf, err := tx.CreateTxWithMetaBlob(txBlob, meta)
	require.NoError(t, err)
	jsonLeaf := marshalStoredTransaction(t, txJSON, tx.MetadataToMap(meta), true)

	for _, stored := range []storedTransactionDataCase{{name: "binary", data: leaf}, {name: "json", data: jsonLeaf}} {
		for _, version := range []int{1, 2} {
			for _, binary := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/v%d/binary=%t", stored.name, version, binary), func(t *testing.T) {
					reader := newDefaultLedgerReader(2, true)
					reader.transactions = append(reader.transactions, struct {
						hash [32]byte
						data []byte
					}{hash: hash, data: stored.data})
					mock := newMockLedgerServiceTx()
					mock.transactions[hashString] = &types.TransactionInfo{
						TxData: stored.data, LedgerIndex: 2, Validated: true,
					}
					mock.getLedgerBySequenceFn = func(uint32) (types.LedgerReader, error) { return reader, nil }
					ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleUser, ApiVersion: version, Services: servicesForTx(mock)}
					params, err := json.Marshal(map[string]any{"transaction": hashString, "binary": binary})
					require.NoError(t, err)
					result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
					require.Nil(t, rpcErr)
					response := resultToMap(t, result)
					require.Equal(t, true, response["validated"])
					metaField := "meta"
					if binary && version > 1 {
						metaField = "meta_blob"
					}
					assertMetadata := func(value any) {
						t.Helper()
						if binary {
							require.Equal(t, strings.ToUpper(hex.EncodeToString(metaBlob)), value)
							return
						}
						fields := maps.Clone(value.(map[string]any))
						if ids, ok := fields["nftoken_ids"]; ok {
							require.Empty(t, ids)
							delete(fields, "nftoken_ids")
						}
						actual, err := json.Marshal(fields)
						require.NoError(t, err)
						require.JSONEq(t, string(wantMetaJSON), string(actual))
					}
					assertMetadata(response[metaField])

					params, err = json.Marshal(map[string]any{"ledger_index": 2, "transactions": true, "expand": true, "binary": binary})
					require.NoError(t, err)
					result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, params)
					require.Nil(t, rpcErr)
					ledger := resultToMap(t, result)["ledger"].(map[string]any)
					transactions := ledger["transactions"].([]any)
					require.Len(t, transactions, 1)
					if !binary && version == 1 {
						metaField = "metaData"
					}
					assertMetadata(transactions[0].(map[string]any)[metaField])

					if !binary {
						params, err = json.Marshal(map[string]any{"ledger_index": 2, "tx_hash": hashString})
						require.NoError(t, err)
						result, rpcErr = (&handlers.TransactionEntryMethod{}).Handle(ctx, params)
						require.Nil(t, rpcErr)
						metaField = "meta"
						if version == 1 {
							metaField = "metadata"
						}
						assertMetadata(resultToMap(t, result)[metaField])
					}
				})
			}
		}
	}
}
